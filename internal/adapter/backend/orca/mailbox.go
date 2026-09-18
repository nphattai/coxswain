package orca

import (
	"encoding/json"
	"fmt"

	"github.com/nphattai/coxswain/internal/adapter/backend"
)

// mailbox is the Orca orchestration mailbox. Check reads with `orchestration check --run <run>`, the run-scoped
// FIFO-batch reader: it returns the current delivery batch and its delivery id but does NOT advance it - Orca replays
// the same batch until `--ack <delivery_id>`, so a Check alone never consumes mail (F06 intent). Ack is the separate,
// explicit acknowledgement.
//
// This is what keeps Orca's own terminal doorbell quiet. `orchestration inbox` is a read-only listing that never acks,
// so Orca kept re-ringing "You have N orchestration messages" at the leader terminal. The watcher instead Checks then
// Acks the delivery id once every wake is durably written (watch.go), draining Orca's check queue so the bell stops.
type mailbox struct {
	c *Client
}

func (m *mailbox) Send(to, subject, body string) error {
	args := []string{"orchestration", "send", "--to", to, "--subject", subject, "--body", body, "--type", "status", "--json"}
	if m.c.Run != "" {
		args = append(args, "--run", m.c.Run)
	}
	if m.c.From != "" {
		args = append(args, "--from", m.c.From)
	}
	_, err := m.c.call(args...)
	return err
}

// Check returns the run's current delivery batch and its delivery id without acking it (Orca replays until --ack, so
// Check never consumes). The reader is run-scoped, but each message is still filtered to to_handle == "run:<run>" so a
// stray cross-run delivery never wakes this epic. A blank Run is a hard error, and the delivery id is returned for the
// watcher to Ack after the wakes are durably written.
func (m *mailbox) Check() ([]backend.Message, string, error) {
	if m.c.Run == "" {
		return nil, "", fmt.Errorf("orca mailbox Check: no run id; refusing to read mail unscoped")
	}
	res, err := m.c.call("orchestration", "check", "--run", m.c.Run, "--json")
	if err != nil {
		return nil, "", err
	}
	var r struct {
		DeliveryID string `json:"deliveryId"`
		Messages   []struct {
			ID        string `json:"id"`
			From      string `json:"from_handle"`
			To        string `json:"to_handle"`
			Subject   string `json:"subject"`
			Body      string `json:"body"`
			Type      string `json:"type"`
			Payload   string `json:"payload"`
			CreatedAt string `json:"created_at"`
			Read      int    `json:"read"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, "", fmt.Errorf("parse check result: %w", err)
	}
	want := "run:" + m.c.Run
	msgs := make([]backend.Message, 0, len(r.Messages))
	for _, x := range r.Messages {
		if x.To != want {
			continue // another run's mail; not this epic's to wake on
		}
		msgs = append(msgs, backend.Message{
			ID: x.ID, From: x.From, Subject: x.Subject, Body: x.Body, Type: x.Type,
			Payload: x.Payload, CreatedAt: x.CreatedAt, Read: x.Read != 0,
		})
	}
	return msgs, r.DeliveryID, nil
}

func (m *mailbox) Reply(msgID, body string) error {
	args := []string{"orchestration", "reply", "--id", msgID, "--body", body, "--json"}
	if m.c.Run != "" {
		args = append(args, "--run", m.c.Run)
	}
	if m.c.From != "" {
		args = append(args, "--from", m.c.From)
	}
	_, err := m.c.mutate(args...)
	return err
}

// Ack acknowledges a delivery batch obtained from a prior `orchestration check`. A blank id is a no-op so callers can
// safely Ack the empty id Check returns.
func (m *mailbox) Ack(deliveryID string) error {
	if deliveryID == "" {
		return nil
	}
	args := []string{"orchestration", "check", "--ack", deliveryID, "--json"}
	if m.c.Run != "" {
		args = append(args, "--run", m.c.Run)
	}
	_, err := m.c.call(args...)
	return err
}
