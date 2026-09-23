package qlog

import (
	"context"

	"github.com/aegispanel/nodeagent/internal/realityquic"
	"github.com/aegispanel/nodeagent/internal/realityquic/qlog"
	"github.com/aegispanel/nodeagent/internal/realityquic/qlogwriter"
)

const EventSchema = "urn:ietf:params:qlog:events:http3-12"

func DefaultConnectionTracer(ctx context.Context, isClient bool, connID quic.ConnectionID) qlogwriter.Trace {
	return qlog.DefaultConnectionTracerWithSchemas(ctx, isClient, connID, []string{qlog.EventSchema, EventSchema})
}
