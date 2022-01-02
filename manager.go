package graceful

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// manager represents the graceful server manager interface
var manager *Manager

// startOnce initial graceful manager once
var startOnce = sync.Once{}

type (
	RunningJob func(context.Context) error
	ShtdownJob func() error
)

// Manager manages the graceful shutdown process
type Manager struct {
	lock              *sync.RWMutex
	shutdownCtx       context.Context
	shutdownCtxCancel context.CancelFunc
	doneCtx           context.Context
	doneCtxCancel     context.CancelFunc
	logger            Logger
	runningWaitGroup  sync.WaitGroup
	errors            []error
	runAtShutdown     []func() error
}

func (g *Manager) start(ctx context.Context) {
	g.shutdownCtx, g.shutdownCtxCancel = context.WithCancel(ctx)
	g.doneCtx, g.doneCtxCancel = context.WithCancel(ctx)

	go g.handleSignals(ctx)
}

// doGracefulShutdown graceful shutdown all task
func (g *Manager) doGracefulShutdown() {
	g.shutdownCtxCancel()
	// doing shutdown job
	for _, f := range g.runAtShutdown {
		g.doShutdownJob(f)
	}
	go func() {
		g.waitForJobs()
		g.lock.Lock()
		g.doneCtxCancel()
		g.lock.Unlock()
	}()
}

func (g *Manager) waitForJobs() {
	g.runningWaitGroup.Wait()
}

func (g *Manager) handleSignals(ctx context.Context) {
	c := make(chan os.Signal, 1)

	signal.Notify(
		c,
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer signal.Stop(c)

	pid := syscall.Getpid()
	for {
		select {
		case sig := <-c:
			switch sig {
			case syscall.SIGINT:
				g.logger.Infof("PID %d. Received SIGINT. Shutting down...", pid)
				g.doGracefulShutdown()
			case syscall.SIGTERM:
				g.logger.Infof("PID %d. Received SIGTERM. Shutting down...", pid)
				g.doGracefulShutdown()
			default:
				g.logger.Infof("PID %d. Received %v.", pid, sig)
			}
		case <-ctx.Done():
			g.logger.Infof("PID: %d. Background context for manager closed - %v - Shutting down...", pid, ctx.Err())
			g.doGracefulShutdown()
		}
	}
}

// doShutdownJob execute shutdown task
func (g *Manager) doShutdownJob(f ShtdownJob) {
	go func() {
		// to handle panic cases from inside the worker
		defer func() {
			g.runningWaitGroup.Done()
			if err := recover(); err != nil {
				g.logger.Error(err)
				g.lock.Lock()
				g.errors = append(g.errors, fmt.Errorf("panic in shutdown job: %v", err))
				g.lock.Unlock()
			}
		}()
		if err := f(); err != nil {
			g.lock.Lock()
			g.errors = append(g.errors, err)
			g.lock.Unlock()
		}
	}()
}

// AddShutdownJob add shutdown task
func (g *Manager) AddShutdownJob(f ShtdownJob) {
	g.runningWaitGroup.Add(1)
	g.lock.Lock()
	g.runAtShutdown = append(g.runAtShutdown, f)
	g.lock.Unlock()
}

// AddRunningJob add running task
func (g *Manager) AddRunningJob(f RunningJob) {
	g.runningWaitGroup.Add(1)

	go func() {
		// to handle panic cases from inside the worker
		defer func() {
			g.runningWaitGroup.Done()
			if err := recover(); err != nil {
				g.logger.Error(err)
				g.lock.Lock()
				g.errors = append(g.errors, fmt.Errorf("panic in running job: %v", err))
				g.lock.Unlock()
			}
		}()
		if err := f(g.shutdownCtx); err != nil {
			g.lock.Lock()
			g.errors = append(g.errors, err)
			g.lock.Unlock()
		}
	}()
}

// Done allows the manager to be viewed as a context.Context.
func (g *Manager) Done() <-chan struct{} {
	return g.doneCtx.Done()
}

// NewManager initial the Manager
func NewManager(opts ...Option) *Manager {
	o := newOptions(opts...)
	startOnce.Do(func() {
		manager = &Manager{
			lock:   &sync.RWMutex{},
			logger: o.logger,
			errors: make([]error, 0),
		}
	})

	manager.start(o.ctx)

	return manager
}

// NewManager get the Manager
func GetManager() *Manager {
	if manager == nil {
		panic("please new the manager first")
	}

	return manager
}
