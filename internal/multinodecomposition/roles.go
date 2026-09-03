package multinodecomposition

import (
	"context"
	"time"

	"bria/internal/nodebootstrap"
	"bria/internal/nodelink"
)

type CoordinatorOptions struct {
	Listener  nodelink.RawListener
	Identity  nodelink.CoordinatorTLSIdentity
	Pairings  *nodelink.PairingFile
	Bootstrap *nodebootstrap.BootstrapService
	Demux     nodebootstrap.DemuxLimits
	Network   nodelink.ServerLimits
	Handle    func(context.Context, *nodelink.SecureChannel) error
}

type CoordinatorRole struct {
	demux     *nodebootstrap.ProtocolDemux
	identity  nodelink.CoordinatorTLSIdentity
	pairings  *nodelink.PairingFile
	bootstrap *nodebootstrap.BootstrapService
	network   nodelink.ServerLimits
	handle    func(context.Context, *nodelink.SecureChannel) error
}

func NewCoordinatorRole(options CoordinatorOptions) (*CoordinatorRole, error) {
	if options.Pairings == nil || options.Bootstrap == nil || options.Handle == nil {
		return nil, ErrInvalidComposition
	}
	demux, err := nodebootstrap.NewProtocolDemux(options.Listener, options.Demux)
	if err != nil {
		return nil, err
	}
	if options.Network.MaxConcurrentConnections <= 0 || options.Network.HandshakeTimeout <= 0 {
		return nil, ErrInvalidComposition
	}
	return &CoordinatorRole{demux: demux, identity: options.Identity, pairings: options.Pairings, bootstrap: options.Bootstrap, network: options.Network, handle: options.Handle}, nil
}

// Run serves pairing and established executor channels through one listener.
func (role *CoordinatorRole) Run(ctx context.Context) error {
	if role == nil || ctx == nil {
		return ErrInvalidComposition
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, 3)
	go func() { results <- role.demux.Run(runCtx) }()
	go func() {
		results <- nodebootstrap.ServeBootstrap(runCtx, role.demux.BootstrapListener(), role.identity, role.bootstrap, role.network)
	}()
	go func() {
		results <- nodelink.ServeCoordinatorWithLimits(runCtx, role.demux.PairedListener(), role.identity, role.pairings.ComputerForFingerprint, role.network, role.handle)
	}()
	first := <-results
	cancel()
	for count := 1; count < 3; count++ {
		<-results
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if first == nil {
		return ErrInvalidComposition
	}
	return first
}

type ExecutorOptions struct {
	Dialer             nodelink.RawDialer
	CoordinatorAddress string
	Identity           nodelink.TLSIdentity
	OutboxPath         string
	MaxFrameBytes      uint32
	RetryDelay         time.Duration
}

type ExecutorRole struct {
	connector   *nodelink.ExecutorConnector
	outbox      *FileEventOutbox
	executorID  string
	coordinator string
}

func NewExecutorRole(options ExecutorOptions) (*ExecutorRole, error) {
	outbox, err := OpenFileEventOutbox(options.OutboxPath)
	if err != nil {
		return nil, err
	}
	connector, err := nodelink.NewExecutorConnector(nodebootstrap.ProtocolDialer{Dialer: options.Dialer, Protocol: nodebootstrap.ProtocolPaired}, options.CoordinatorAddress, options.Identity, options.MaxFrameBytes, options.RetryDelay)
	if err != nil {
		return nil, err
	}
	return &ExecutorRole{connector: connector, outbox: outbox, executorID: string(options.Identity.LocalComputerID), coordinator: string(options.Identity.PeerComputerID)}, nil
}

func (role *ExecutorRole) EnqueueEvent(ctx context.Context, event nodelink.Envelope) error {
	if role == nil || role.outbox == nil {
		return ErrInvalidComposition
	}
	if string(event.SourceComputerID) != role.executorID || string(event.CoordinatorID) != role.coordinator || event.TargetComputerID != event.CoordinatorID {
		return ErrInvalidComposition
	}
	return role.outbox.Enqueue(ctx, event)
}

func (role *ExecutorRole) PendingEvents() []nodelink.Envelope {
	if role == nil || role.outbox == nil {
		return nil
	}
	return role.outbox.Pending()
}

func (role *ExecutorRole) Run(ctx context.Context) error {
	if role == nil || role.connector == nil || role.outbox == nil || ctx == nil {
		return ErrInvalidComposition
	}
	return role.connector.Run(ctx, role.outbox.Deliver)
}
