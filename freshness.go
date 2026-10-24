package main

import "time"

// Notification reports only an SMTP relay's result, never recipient delivery.
// Configuration and a successful no-op round are not delivery evidence.
type Notification struct {
	State       string `json:"state"`
	AttemptedAt int64  `json:"attemptedAt"`
	AcceptedAt  int64  `json:"acceptedAt"`
}

type Monitoring struct {
	State         string       `json:"state"`
	CheckedAt     int64        `json:"checkedAt"`
	FreshUntil    int64        `json:"freshUntil"`
	Failure       string       `json:"failure,omitempty"`
	Notifications Notification `json:"notifications"`
}

type monitoringMail struct {
	started, accepted            time.Time
	inFlight, failed, overlapped bool
}

func (w *Watcher) timeNow() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

func (w *Watcher) monitoringAt(now time.Time) Monitoring {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := Monitoring{State: "starting", Notifications: Notification{State: "disabled"}}
	if w.mailer.Configured() {
		out.Notifications.State = "unverified"
		if !w.mail.started.IsZero() {
			out.Notifications.AttemptedAt = w.mail.started.Unix()
		}
		if !w.mail.accepted.IsZero() {
			out.Notifications.AcceptedAt = w.mail.accepted.Unix()
			out.Notifications.State = "accepted"
		}
		if w.mail.inFlight {
			out.Notifications.State = "pending"
		}
		if w.mail.failed || (w.mail.inFlight && !now.Before(w.mail.started.Add(notificationTimeout))) {
			out.Notifications.State = "failed"
		}
	}
	if w.every < time.Second || w.every > (time.Duration(1<<63-1)-time.Minute)/2 {
		out.State, out.Failure = "failed", "invalid_interval"
		return out
	}
	if !w.lastAt.IsZero() {
		out.CheckedAt = w.lastAt.Unix()
		budget := 2*w.every + time.Minute
		out.FreshUntil = w.lastAt.Add(budget).Unix()
		expired := false
		if !w.leaseStarted.IsZero() {
			elapsed := time.Since(w.leaseStarted)
			if w.leaseAge != nil {
				elapsed = w.leaseAge(w.leaseStarted)
			}
			expired = elapsed < 0 || elapsed >= budget
		}
		switch {
		case now.Before(w.lastAt):
			out.State, out.Failure = "failed", "clock_moved_back"
		case expired || !now.Before(time.Unix(out.FreshUntil, 0)):
			out.State = "stale"
		case w.roundFailed:
			out.State, out.Failure = "failed", "history_write_failed"
		case len(w.comps) == 0 || len(w.last) != len(w.comps):
			out.State, out.Failure = "failed", "incomplete_round"
		default:
			out.State = "fresh"
		}
	}
	if out.State == "fresh" && out.Notifications.State == "failed" {
		out.State, out.Failure = "failed", "notification_failed"
	}
	if out.State == "fresh" && (w.mail.started.After(now) || w.mail.accepted.After(now)) {
		out.State, out.Failure = "failed", "clock_moved_back"
	}
	if w.stopped {
		out.State = "stopped"
	}
	return out
}
