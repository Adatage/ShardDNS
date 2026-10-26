package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	sharddnsv1 "github.com/Adatage/ShardDNS/api/sharddns/v1"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
)

type Bus struct {
	nc      *nats.Conn
	subject string
	origin  string
	logger  *slog.Logger
}

func Connect(url, subject string, onReconnect func(), logger *slog.Logger) (*Bus, error) {
	if logger == nil {
		logger = slog.Default()
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	opts := []nats.Option{
		nats.Name("sharddnsd"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
		nats.Timeout(5 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			logger.Warn("NATS disconnected", "err", err)
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			logger.Info("NATS reconnected", "url", c.ConnectedUrl())
			if onReconnect != nil {
				onReconnect()
			}
		}),
	}
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("events: connect: %w", err)
	}
	return &Bus{nc: nc, subject: subject, origin: hex.EncodeToString(id[:]), logger: logger}, nil
}

func (b *Bus) Publish(_ context.Context, kind sharddnsv1.ChangeEvent_Kind, zone, name string) {
	data, err := proto.Marshal(&sharddnsv1.ChangeEvent{Origin: b.origin, Kind: kind, Zone: zone, Name: name})
	if err != nil {
		b.logger.Error("marshal change event", "err", err)
		return
	}
	if err := b.nc.Publish(b.subject, data); err != nil {
		b.logger.Warn("publish change event", "err", err)
	}
}

func (b *Bus) Subscribe(handler func(*sharddnsv1.ChangeEvent)) error {
	_, err := b.nc.Subscribe(b.subject, func(msg *nats.Msg) {
		var ev sharddnsv1.ChangeEvent
		if err := proto.Unmarshal(msg.Data, &ev); err != nil {
			b.logger.Warn("invalid change event", "err", err)
			return
		}
		if ev.GetOrigin() == b.origin {
			return
		}
		handler(&ev)
	})
	return err
}

func (b *Bus) Healthy() bool {
	return b.nc.Status() == nats.CONNECTED
}

func (b *Bus) Close() {
	_ = b.nc.Drain()
}
