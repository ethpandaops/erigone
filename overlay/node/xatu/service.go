// Copyright 2024 The Erigon Authors
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

//go:build embedded

// Package xatu implements the Xatu execution processor integration for embedded mode.
package xatu

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"github.com/creasty/defaults"
	"github.com/ethpandaops/execution-processor/pkg/config"
	"github.com/ethpandaops/execution-processor/pkg/ethereum"
	"github.com/ethpandaops/execution-processor/pkg/ethereum/execution"
	"github.com/ethpandaops/execution-processor/pkg/processor"
	"github.com/ethpandaops/execution-processor/pkg/redis"
	"github.com/ethpandaops/execution-processor/pkg/state"
	r "github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	"github.com/erigontech/erigon/common/log/v3"
	"github.com/erigontech/erigon/db/kv"
	"github.com/erigontech/erigon/db/services"
	"github.com/erigontech/erigon/execution/chain"
	"github.com/erigontech/erigon/execution/protocol/rules"
	"github.com/erigontech/erigon/node"
)

// Config holds Xatu service configuration.
type Config struct {
	ConfigPath     string
	SimulationOnly bool // If true, only enable simulation RPC endpoints without execution-processor
}

// Service implements the Xatu execution processor integration.
// It implements the execution.DataSource interface to provide direct
// data access without JSON-RPC overhead.
type Service struct {
	config      Config
	db          kv.TemporalRoDB
	blockReader services.FullBlockReader
	chainConfig *chain.Config
	engine      rules.EngineReader

	// dbChainConfig is the chain config read from the database, which may differ
	// from the in-memory chainConfig if the DB was updated after node init (e.g.,
	// fork schedule changes). Lazily loaded on first use via dbChainConfigOnce.
	dbChainConfig     *chain.Config
	dbChainConfigOnce sync.Once
	dbChainConfigErr  error

	// execution-processor components
	embeddedNode *execution.EmbeddedNode
	pool         *ethereum.Pool
	manager      *processor.Manager
	stateManager *state.Manager
	redisClient  *r.Client

	ctx       context.Context
	ctxCancel context.CancelFunc
	wg        sync.WaitGroup
	log       log.Logger
	synced    atomic.Bool
}

// New creates and registers the Xatu service with the node.
// Returns the service instance so the caller can access it for API registration.
func New(
	n *node.Node,
	db kv.TemporalRoDB,
	blockReader services.FullBlockReader,
	chainConfig *chain.Config,
	engine rules.EngineReader,
	config Config,
	logger log.Logger,
) (*Service, error) {
	svc := &Service{
		config:      config,
		db:          db,
		blockReader: blockReader,
		chainConfig: chainConfig,
		engine:      engine,
		log:         logger.New("service", "xatu"),
	}

	n.RegisterLifecycle(svc)

	return svc, nil
}

// loadConfig loads the config from file.
func loadConfig(file string) (*config.Config, error) {
	cfg := &config.Config{}

	if err := defaults.Set(cfg); err != nil {
		return nil, err
	}

	yamlFile, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	type plain config.Config

	if err := yaml.Unmarshal(yamlFile, (*plain)(cfg)); err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

// Start implements node.Lifecycle, starting the Xatu service.
func (s *Service) Start() error {
	// Simulation-only mode: skip execution-processor setup, only enable RPC endpoints
	if s.config.SimulationOnly {
		s.log.Info("Xatu service started in simulation-only mode")
		return nil
	}

	// Load config from file
	cfg, err := loadConfig(s.config.ConfigPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Create logrus logger for execution-processor (it uses logrus)
	logrusLog := logrus.New()

	level, err := logrus.ParseLevel(cfg.LoggingLevel)
	if err != nil {
		level = logrus.InfoLevel
	}

	logrusLog.SetLevel(level)
	fieldLogger := logrusLog.WithField("component", "xatu")

	// Create Redis client
	if cfg.Redis == nil {
		return fmt.Errorf("redis configuration is required")
	}

	s.redisClient, err = redis.New(cfg.Redis)
	if err != nil {
		return fmt.Errorf("failed to create redis client: %w", err)
	}

	// Create cancellable context for lifecycle management
	s.ctx, s.ctxCancel = context.WithCancel(context.Background())
	ctx := s.ctx

	s.stateManager, err = state.NewManager(fieldLogger.WithField("component", "state"), &cfg.StateManager)
	if err != nil {
		return fmt.Errorf("failed to create state manager: %w", err)
	}

	// Create embedded node with this service as the DataSource
	s.embeddedNode = execution.NewEmbeddedNode(fieldLogger.WithField("component", "embedded"), "erigon-embedded", s)

	// Create pool with the embedded node
	nodes := []execution.Node{s.embeddedNode}

	// Convert config.EthereumConfig to ethereum.Config (same structure)
	ethConfig := &ethereum.Config{
		Execution:           cfg.Ethereum.Execution,
		OverrideNetworkName: cfg.Ethereum.OverrideNetworkName,
	}

	s.pool = ethereum.NewPoolWithNodes(fieldLogger.WithField("component", "pool"), "xatu", nodes, ethConfig)

	// Create processor manager
	s.manager, err = processor.NewManager(
		fieldLogger.WithField("component", "processor"),
		&cfg.Processors,
		s.pool,
		s.stateManager,
		s.redisClient,
		cfg.Redis.Prefix,
	)
	if err != nil {
		return fmt.Errorf("failed to create processor manager: %w", err)
	}

	// Start the pool
	s.pool.Start(ctx)

	// Start the state manager
	s.wg.Add(1)

	go func() {
		defer s.wg.Done()

		if err := s.stateManager.Start(ctx); err != nil {
			s.log.Error("State manager error", "err", err)
		}
	}()

	// Start the manager in a goroutine
	s.wg.Add(1)

	go func() {
		defer s.wg.Done()

		if err := s.manager.Start(ctx); err != nil {
			s.log.Error("Execution processor manager error", "err", err)
		}
	}()

	// Mark the embedded node as ready - the DataSource can serve requests immediately.
	// Processing will naturally only process blocks that exist.
	if err := s.embeddedNode.MarkReady(ctx); err != nil {
		return fmt.Errorf("failed to mark embedded node as ready: %w", err)
	}

	s.log.Info("Xatu service started")

	return nil
}

// Stop implements node.Lifecycle, stopping the Xatu service.
func (s *Service) Stop() error {
	// Cancel the context to signal all goroutines to stop
	if s.ctxCancel != nil {
		s.ctxCancel()
	}

	ctx := context.Background()

	if s.manager != nil {
		if err := s.manager.Stop(ctx); err != nil {
			s.log.Warn("Failed to stop execution-processor manager", "err", err)
		}
	}

	if s.stateManager != nil {
		if err := s.stateManager.Stop(ctx); err != nil {
			s.log.Warn("Failed to stop state manager", "err", err)
		}
	}

	if s.pool != nil {
		if err := s.pool.Stop(ctx); err != nil {
			s.log.Warn("Failed to stop pool", "err", err)
		}
	}

	if s.redisClient != nil {
		if err := s.redisClient.Close(); err != nil {
			s.log.Warn("Failed to close redis client", "err", err)
		}
	}

	s.wg.Wait()
	s.log.Info("Xatu service stopped")

	return nil
}

// SetSynced is called by Erigon when sync completes.
// This marks the embedded node as ready.
func (s *Service) SetSynced(synced bool) {
	wasSynced := s.synced.Swap(synced)
	if synced && !wasSynced && s.embeddedNode != nil {
		ctx := context.Background()
		if err := s.embeddedNode.MarkReady(ctx); err != nil {
			s.log.Error("Failed to mark embedded node as ready", "err", err)
		} else {
			s.log.Info("EmbeddedNode marked as ready")
		}
	}
}
