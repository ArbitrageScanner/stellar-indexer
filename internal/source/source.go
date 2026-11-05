// Package source provides LedgerCloseMeta from a Stellar RPC server.
package source

import (
	"context"
	"fmt"
	"net/http"
	"time"

	rpc "github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/ingest/ledgerbackend"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// Source yields ledgers in strictly increasing order.
type Source interface {
	// Prepare positions the source at from; to == 0 means unbounded.
	Prepare(ctx context.Context, from, to uint32) error
	// GetLedger blocks until the ledger is available (live tail) or ctx ends.
	GetLedger(ctx context.Context, seq uint32) (xdr.LedgerCloseMeta, error)
	Window(ctx context.Context) (oldest, latest uint32, err error)
	Close() error
}

type RPCSource struct {
	url     string
	buffer  uint32
	http    *http.Client
	client  *rpc.Client
	backend *ledgerbackend.RPCLedgerBackend
}

func NewRPC(url string, buffer uint32, timeout time.Duration) *RPCSource {
	hc := &http.Client{Timeout: timeout}
	return &RPCSource{url: url, buffer: buffer, http: hc, client: rpc.NewClient(url, hc)}
}

func (s *RPCSource) Prepare(ctx context.Context, from, to uint32) error {
	if s.backend != nil {
		_ = s.backend.Close()
	}
	s.backend = ledgerbackend.NewRPCLedgerBackend(ledgerbackend.RPCLedgerBackendOptions{
		RPCServerURL: s.url,
		BufferSize:   s.buffer,
		HttpClient:   s.http,
	})
	r := ledgerbackend.UnboundedRange(from)
	if to != 0 {
		r = ledgerbackend.BoundedRange(from, to)
	}
	if err := s.backend.PrepareRange(ctx, r); err != nil {
		return fmt.Errorf("prepare range from %d: %w", from, err)
	}
	return nil
}

func (s *RPCSource) GetLedger(ctx context.Context, seq uint32) (xdr.LedgerCloseMeta, error) {
	if s.backend == nil {
		return xdr.LedgerCloseMeta{}, fmt.Errorf("source not prepared")
	}
	return s.backend.GetLedger(ctx, seq)
}

// Window returns the ledger retention window currently served by RPC.
func (s *RPCSource) Window(ctx context.Context) (uint32, uint32, error) {
	h, err := s.client.GetHealth(ctx)
	if err != nil {
		return 0, 0, err
	}
	return h.OldestLedger, h.LatestLedger, nil
}

func (s *RPCSource) Close() error {
	if s.backend != nil {
		return s.backend.Close()
	}
	return nil
}
