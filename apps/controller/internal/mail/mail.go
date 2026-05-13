// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mail wraps net/smtp with a tiny invitation-email surface.
// Empty SMTPConfig.Host disables delivery — Send returns nil + false
// so the caller can fall back to "share the token manually". A real
// SaaS deploy will set BAMBOO_SMTP_* env vars; local dev usually
// leaves it disabled.
package mail

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/smtp"
	"strings"
	"text/template"

	"github.com/hanfour/bamboo/apps/controller/internal/config"
)

// Sender holds the configured SMTP relay. Construct once at server
// boot and pass into handlers; method receivers are pointers so
// future enhancements (TLS, rate limiting) can mutate state without
// breaking callers.
type Sender struct {
	cfg config.SMTPConfig
}

// New constructs a Sender from config. Always returns a non-nil
// Sender — when SMTP is disabled the sender's Send returns
// (false, nil) and logs a debug line so the rest of the controller
// doesn't have to nil-check.
func New(cfg config.SMTPConfig) *Sender {
	return &Sender{cfg: cfg}
}

// Enabled reports whether the sender has enough config to attempt
// delivery. Handlers can use this to render UI hints.
func (s *Sender) Enabled() bool {
	return s != nil && s.cfg.Host != "" && s.cfg.From != ""
}

// SendInvitation delivers the invitation token to the invitee. Returns
// (sent, err): sent=true means the SMTP relay accepted the message;
// sent=false + err=nil means SMTP is disabled (no-op); sent=false +
// err!=nil means delivery failed. Callers should not block the
// surrounding mutation on err — invitation rows persist whether the
// email goes out or not, and the API response carries the plaintext
// token so the admin always has a fallback.
func (s *Sender) SendInvitation(toEmail, inviteURL, tenantSlug, invitedByEmail string) (bool, error) {
	if !s.Enabled() {
		slog.Debug("SMTP disabled; skipping invitation email", "to", toEmail)
		return false, nil
	}
	body, err := renderInvitationBody(invitationData{
		ToEmail:        toEmail,
		InviteURL:      inviteURL,
		TenantSlug:     tenantSlug,
		InvitedByEmail: invitedByEmail,
	})
	if err != nil {
		return false, fmt.Errorf("render invitation body: %w", err)
	}
	msg := buildPlainMessage(s.cfg.From, toEmail, invitationSubject(tenantSlug), body)

	addr := fmt.Sprintf("%s:%d", s.cfg.Host, smtpPort(s.cfg.Port))
	var auth smtp.Auth
	if s.cfg.User != "" {
		auth = smtp.PlainAuth("", s.cfg.User, s.cfg.Pass, s.cfg.Host)
	}
	if err := smtp.SendMail(addr, auth, s.cfg.From, []string{toEmail}, msg); err != nil {
		return false, fmt.Errorf("smtp send: %w", err)
	}
	slog.Info("invitation email sent", "to", toEmail, "tenant", tenantSlug)
	return true, nil
}

// smtpPort defaults to 587 (submission with STARTTLS) when unset.
// Port 25 is intentionally NOT the default — most relays today
// reject unauthenticated 25 from non-loopback sources.
func smtpPort(p int) int {
	if p == 0 {
		return 587
	}
	return p
}

func invitationSubject(tenantSlug string) string {
	return fmt.Sprintf("You're invited to bamboo (%s)", tenantSlug)
}

type invitationData struct {
	ToEmail        string
	InviteURL      string
	TenantSlug     string
	InvitedByEmail string
}

// invitationTpl is a plain-text email body. HTML email is deferred
// — most invite emails get scanned for the link by the recipient's
// client and never rendered as marketing-style HTML, and shipping
// a multipart/alternative pair from a stdlib net/smtp send adds
// boilerplate without changing the outcome.
const invitationTpl = `Hi,

{{if .InvitedByEmail}}{{.InvitedByEmail}} has{{else}}A bamboo admin has{{end}} invited you to join the "{{.TenantSlug}}" tenant on bamboo, an AI-native zero-trust mesh networking service.

To accept the invitation, visit:

  {{.InviteURL}}

You'll be asked to sign in with Google. Use the email address this message was sent to ({{.ToEmail}}); any other account will be rejected.

If you didn't expect this invitation, you can ignore the email — the link expires automatically.

— bamboo
`

func renderInvitationBody(d invitationData) (string, error) {
	tpl, err := template.New("invitation").Parse(invitationTpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, d); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// buildPlainMessage wraps a plain-text body in the minimal RFC 5322
// envelope expected by net/smtp.SendMail. Crucially: \r\n line
// endings, From/To/Subject/MIME headers, then blank line, then body.
func buildPlainMessage(from, to, subject, body string) []byte {
	var buf strings.Builder
	buf.WriteString("From: ")
	buf.WriteString(from)
	buf.WriteString("\r\n")
	buf.WriteString("To: ")
	buf.WriteString(to)
	buf.WriteString("\r\n")
	buf.WriteString("Subject: ")
	buf.WriteString(subject)
	buf.WriteString("\r\n")
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	buf.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	buf.WriteString("\r\n")
	buf.WriteString(body)
	return []byte(buf.String())
}
