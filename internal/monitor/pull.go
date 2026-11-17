package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"google.golang.org/api/pubsub/v1"
)

type (
	pulledMessage struct{ AckID, MessageID, Data string }
	pullSource    interface {
		Pull(context.Context, string) ([]pulledMessage, error)
		Ack(context.Context, string, []string) error
	}
)
type googlePull struct{ service *pubsub.Service }

func (source googlePull) Pull(ctx context.Context, subscription string) ([]pulledMessage, error) {
	response, err := source.service.Projects.Subscriptions.Pull(subscription, &pubsub.PullRequest{MaxMessages: 10}).
		Context(ctx).
		Do()
	if err != nil {
		return nil, err
	}
	messages := make([]pulledMessage, 0, len(response.ReceivedMessages))
	for _, message := range response.ReceivedMessages {
		if message.Message != nil {
			messages = append(
				messages,
				pulledMessage{
					AckID:     message.AckId,
					MessageID: message.Message.MessageId,
					Data:      message.Message.Data,
				},
			)
		}
	}
	return messages, nil
}

func (source googlePull) Ack(ctx context.Context, subscription string, ids []string) error {
	_, err := source.service.Projects.Subscriptions.Acknowledge(subscription, &pubsub.AcknowledgeRequest{AckIds: ids}).
		Context(ctx).
		Do()
	return err
}

func (server *Server) workerStatus(state, message string, polled, received bool) {
	server.workerMu.Lock()
	defer server.workerMu.Unlock()
	server.worker.State, server.worker.Error = state, message
	now := time.Now().UTC()
	if polled {
		server.worker.LastPoll = &now
	}
	if received {
		server.worker.LastMessage = &now
	}
}

func (server *Server) processMessages(
	ctx context.Context,
	subscription string,
	source pullSource,
	messages []pulledMessage,
) error {
	ids := []string{}
	invalid := false
	for _, message := range messages {
		var envelope Envelope
		envelope.Subscription = subscription
		envelope.Message.ID = message.MessageID
		envelope.Message.Data = message.Data
		body, _ := json.Marshal(envelope)
		alert, err := ParseAlert(body)
		if err != nil {
			invalid = true
			slog.Warn(
				"invalid Monitoring message left unacknowledged",
				"messageId",
				message.MessageID,
			)
			continue
		}
		if _, _, err = server.store().Ingest(ctx, alert); err != nil {
			return errors.New("database commit failed; messages remain unacknowledged")
		}
		ids = append(ids, message.AckID)
	}
	if len(ids) > 0 {
		if err := source.Ack(ctx, subscription, ids); err != nil {
			return errors.New("acknowledgement failed; committed messages may be redelivered")
		}
	}
	if invalid {
		return errors.New("malformed notification left for retry; inspect dead-letter subscription")
	}
	server.workerStatus("receiving", "", true, len(ids) > 0)
	return nil
}

func waitWorker(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (server *Server) RunPull(ctx context.Context) {
	var source pullSource
	for ctx.Err() == nil {
		if server.store() == nil {
			server.workerStatus("setup", "", false, false)
			if !waitWorker(ctx, 2*time.Second) {
				return
			}
			continue
		}
		settings, err := server.store().Settings(ctx)
		if err != nil || !settings.Enabled {
			server.workerStatus("paused", "", false, false)
			if !waitWorker(ctx, 2*time.Second) {
				return
			}
			continue
		}
		if source == nil {
			service, err := pubsub.NewService(ctx)
			if err != nil {
				server.workerStatus(
					"error",
					"Runtime identity unavailable; attach a service account with Pub/Sub Subscriber permission.",
					false,
					false,
				)
				if !waitWorker(ctx, 15*time.Second) {
					return
				}
				continue
			}
			source = googlePull{service: service}
		}
		if err := server.pullOnce(ctx, settings, source); err != nil && ctx.Err() == nil {
			server.workerStatus("error", err.Error(), false, false)
			if !waitWorker(ctx, 5*time.Second) {
				return
			}
		}
	}
}

func (server *Server) pullOnce(ctx context.Context, settings Settings, source pullSource) error {
	lease, err := server.store().Pool.Acquire(ctx)
	if err != nil {
		return errors.New("database unavailable")
	}
	defer lease.Release()
	var locked bool
	key := "signaldesk-pull:" + settings.WorkspaceID
	if err = lease.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,0))", key).
		Scan(&locked); err != nil {
		return errors.New("cannot acquire consumer lease")
	}
	if !locked {
		server.workerStatus(
			"standby",
			"Another instance holds the workspace consumer lease.",
			false,
			false,
		)
		waitWorker(ctx, 2*time.Second)
		return nil
	}
	defer func() {
		unlock, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := lease.Exec(
			unlock,
			"SELECT pg_advisory_unlock(hashtextextended($1,0))",
			key,
		); err != nil {
			_ = lease.Conn().Close(unlock)
		}
	}()
	pullCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	server.workerStatus("polling", "", false, false)
	messages, err := source.Pull(pullCtx, settings.Subscription)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return errors.New(
			"pull failed; check subscription, runtime identity, network and Pub/Sub Subscriber permission",
		)
	}
	latest, err := server.store().Settings(ctx)
	if err != nil {
		return errors.New("cannot read subscription settings")
	}
	if !latest.Enabled || latest.Subscription != settings.Subscription {
		return nil
	}
	commitCtx, commitCancel := context.WithTimeout(ctx, 20*time.Second)
	defer commitCancel()
	return server.processMessages(commitCtx, settings.Subscription, source, messages)
}
