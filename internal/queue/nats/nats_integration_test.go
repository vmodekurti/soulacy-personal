package nats

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/soulacy/soulacy/internal/queue"
)

func startNATSTestContainer(ctx context.Context) (container testcontainers.Container, url string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("Docker discovery failed: %v", recovered)
		}
	}()
	container, err = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Started: true,
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "nats:2.10-alpine",
			ExposedPorts: []string{"4222/tcp"},
			Cmd:          []string{"-js"},
			WaitingFor:   wait.ForLog("Server is ready").WithStartupTimeout(45 * time.Second),
		},
	})
	if err != nil {
		return nil, "", err
	}
	host, err := container.Host(ctx)
	if err != nil {
		return container, "", err
	}
	port, err := container.MappedPort(ctx, "4222/tcp")
	if err != nil {
		return container, "", err
	}
	return container, "nats://" + host + ":" + port.Port(), nil
}

func TestJetStreamRedeliversAnUnacknowledgedIngressMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	container, url, err := startNATSTestContainer(ctx)
	if err != nil {
		t.Skipf("NATS INTEGRATION SKIPPED LOUDLY: Docker/testcontainer unavailable: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })

	backend, err := New(Config{
		URL: url, StreamName: "soulacytest", SubjectPrefix: "soulacytest.>",
		AckWait: 250 * time.Millisecond, MaxDeliver: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if err := backend.Ping(ctx); err != nil {
		t.Fatalf("ping connected NATS backend: %v", err)
	}

	var deliveries atomic.Int64
	done := make(chan struct{}, 1)
	_, err = backend.Subscribe(ctx, "soulacytest.channels.inbound", "gateway-test", func(msg *queue.Message) {
		if deliveries.Add(1) == 1 {
			return // prove the broker retains and redelivers without Ack
		}
		if err := msg.Ack(); err != nil {
			t.Errorf("ack redelivery: %v", err)
		}
		select {
		case done <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Publish(ctx, "soulacytest.channels.inbound", []byte(`{"id":"provider-1"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatalf("message was delivered %d time(s), want redelivery", deliveries.Load())
	}
	if deliveries.Load() < 2 {
		t.Fatalf("deliveries = %d, want at least 2", deliveries.Load())
	}
}
