// receipt-ingest is an OTLP/gRPC logs receiver for Agent Router's access log.
// Each LLM request's record becomes a receipt in the receipts db, and the
// insert's NOTIFY reaches the console through stargate-api's SSE stream.
//
// It speaks plain OTLP, so an OTel Collector can sit in front of it later
// (spec §4.6) without changing either side.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"time"

	"github.com/jbouder/stargate/server/internal/config"
	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/ingest"
	"github.com/jbouder/stargate/server/internal/store"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/grpc"
)

func main() {
	addr := flag.String("addr", ":4317", "OTLP/gRPC listen address")
	refresh := flag.Duration("refresh", 5*time.Second, "how often to reload config from the control plane db")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, err := store.Open(ctx, config.ConfigDB, config.ReceiptsDB)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	var cur gateway.Current
	var mu sync.Mutex
	load := func() error {
		mu.Lock()
		defer mu.Unlock()
		s, err := gateway.LoadSnapshot(ctx, st, demo.Tenant)
		if err == nil {
			cur.Store(s)
		}
		return err
	}
	if err := load(); err != nil {
		log.Fatalf("load config (run `stargate-api migrate` first?): %v", err)
	}
	go func() {
		t := time.NewTicker(*refresh)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := load(); err != nil && ctx.Err() == nil {
					log.Printf("reload config: %v", err)
				}
			}
		}
	}()

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	gs := grpc.NewServer()
	collogs.RegisterLogsServiceServer(gs, &receiver{snap: &cur, store: st, reload: load})
	go func() {
		<-ctx.Done()
		gs.GracefulStop()
	}()
	log.Printf("receipt-ingest listening on %s (OTLP/gRPC logs)", *addr)
	if err := gs.Serve(lis); err != nil {
		log.Fatal(err)
	}
}

type receiver struct {
	collogs.UnimplementedLogsServiceServer
	snap   *gateway.Current
	store  *store.Store
	reload func() error

	mu         sync.Mutex
	lastReload time.Time
}

// snapshotFor returns a snapshot to build these keys' receipts from. A key
// created since the last reload triggers an early one (at most once a
// second), so its first receipts carry its name.
func (r *receiver) snapshotFor(ids []string) *gateway.Snapshot {
	snap := r.snap.Load()
	for _, id := range ids {
		if id == "" || snap.KeyByID(id) != nil {
			continue
		}
		r.mu.Lock()
		due := time.Since(r.lastReload) > time.Second
		if due {
			r.lastReload = time.Now()
		}
		r.mu.Unlock()
		if due {
			if err := r.reload(); err != nil {
				log.Printf("reload config: %v", err)
			}
		}
		return r.snap.Load()
	}
	return snap
}

// Export writes what it can and reports the rest as rejected, so one bad
// record doesn't make Envoy retry the whole batch.
func (r *receiver) Export(ctx context.Context, req *collogs.ExportLogsServiceRequest) (*collogs.ExportLogsServiceResponse, error) {
	var records []map[string]string
	var keyIDs []string
	for _, rl := range req.GetResourceLogs() {
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				attrs := make(map[string]string, len(lr.GetAttributes()))
				for _, kv := range lr.GetAttributes() {
					attrs[kv.GetKey()] = str(kv.GetValue())
				}
				records = append(records, attrs)
				keyIDs = append(keyIDs, ingest.KeyID(attrs))
			}
		}
	}
	snap := r.snapshotFor(keyIDs)
	var rejected int64
	for _, attrs := range records {
		rc, err := ingest.Receipt(snap, attrs)
		if err == nil {
			err = r.store.PutReceipt(ctx, rc)
		}
		if err != nil {
			rejected++
			log.Printf("drop access-log record: %v", err)
		}
	}
	resp := &collogs.ExportLogsServiceResponse{}
	if rejected > 0 {
		resp.PartialSuccess = &collogs.ExportLogsPartialSuccess{RejectedLogRecords: rejected}
	}
	return resp, nil
}

func str(v *commonpb.AnyValue) string {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10)
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(x.DoubleValue, 'f', -1, 64)
	case *commonpb.AnyValue_BoolValue:
		return strconv.FormatBool(x.BoolValue)
	}
	return ""
}
