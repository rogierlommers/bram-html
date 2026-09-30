package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

type Mailer interface {
	SendLoginCode(ctx context.Context, email, code string) error
	SendPageSaved(ctx context.Context, recipients []string, notification PageSavedNotification) error
}

type PageSavedNotification struct {
	OwnerEmail string
	PageTitle  string
	PageURL    string
	Updated    bool
}

type LogMailer struct {
	Logger *logrus.Entry
}

func (m LogMailer) SendLoginCode(_ context.Context, email, code string) error {
	m.Logger.Printf("development login code for %s: %s", email, code)
	return nil
}

func (m LogMailer) SendPageSaved(_ context.Context, recipients []string, notification PageSavedNotification) error {
	m.Logger.Printf("page saved by %s (%s); notifying %s: %s", notification.OwnerEmail, notification.PageTitle, strings.Join(recipients, ", "), notification.PageURL)
	return nil
}

type SMTPMailer struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
}

const loginCodeMailBoundary = "bram-html-login-code"
const pageSavedMailBoundary = "bram-html-page-saved"

func loginCodeMessage(from, email, code string) string {
	escapedCode := html.EscapeString(code)
	htmlBody := fmt.Sprintf(`<!doctype html>
<html lang="en">
  <body style="margin:0;padding:0;background:#fffaf1;color:#213547;font-family:Arial,sans-serif;">
    <table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="background:#fffaf1;padding:32px 16px;">
      <tr><td align="center">
        <table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="max-width:520px;background:#ffffff;border:1px solid #e8e1d8;border-radius:18px;overflow:hidden;">
          <tr><td style="padding:30px 34px 12px;text-align:center;">
            <div style="display:inline-block;padding:9px 12px;border:2px solid #213547;border-radius:12px;background:#ffc857;color:#213547;font-family:monospace;font-weight:bold;">&lt;/&gt;</div>
            <h1 style="margin:22px 0 8px;font-size:26px;line-height:1.2;">Your sign-in code</h1>
            <p style="margin:0;color:#687684;font-size:16px;line-height:1.5;">Use this code to sign in to bram-html.</p>
          </td></tr>
          <tr><td style="padding:18px 34px;text-align:center;">
            <div style="padding:18px;border-radius:14px;background:#edf4ff;color:#213547;font-family:monospace;font-size:32px;font-weight:bold;letter-spacing:8px;">%s</div>
          </td></tr>
          <tr><td style="padding:10px 34px 32px;text-align:center;">
            <p style="margin:0;color:#687684;font-size:14px;line-height:1.6;"><strong style="color:#213547;">This code expires soon</strong> and can only be used once.<br>If you did not request it, you can safely ignore this email.</p>
          </td></tr>
        </table>
        <p style="margin:18px 0 0;color:#8a959f;font-size:12px;">bram-html &middot; Build, learn, create.</p>
      </td></tr>
    </table>
  </body>
</html>
`, escapedCode)
	htmlBody = strings.ReplaceAll(htmlBody, "\n", "\r\n")

	textBody := fmt.Sprintf("Your bram-html sign-in code is: %s\r\n\r\nThis code expires soon and can only be used once. If you did not request it, you can safely ignore this email.\r\n", code)
	return multipartMessage(from, []string{email}, "Your bram-html sign-in code", loginCodeMailBoundary, "7bit", textBody, htmlBody)
}

func pageSavedMessage(from string, recipients []string, notification PageSavedNotification) string {
	action := "saved a new page"
	if notification.Updated {
		action = "updated a page"
	}
	escapedTitle := html.EscapeString(notification.PageTitle)
	escapedOwner := html.EscapeString(notification.OwnerEmail)
	escapedURL := html.EscapeString(notification.PageURL)
	htmlBody := fmt.Sprintf(`<!doctype html>
<html lang="en">
  <body style="margin:0;padding:0;background:#fffaf1;color:#213547;font-family:Arial,sans-serif;">
    <table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="background:#fffaf1;padding:32px 16px;">
      <tr><td align="center">
        <table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="max-width:560px;background:#ffffff;border:1px solid #e8e1d8;border-radius:18px;overflow:hidden;">
          <tr><td style="padding:32px 36px;">
            <div style="display:inline-block;padding:9px 12px;border:2px solid #213547;border-radius:12px;background:#ffc857;font-family:monospace;font-weight:bold;">&lt;/&gt;</div>
            <h1 style="margin:22px 0 10px;font-size:26px;line-height:1.2;">A page was saved</h1>
            <p style="margin:0 0 20px;color:#687684;font-size:16px;line-height:1.6;"><strong style="color:#213547;">%s</strong> %s:</p>
            <div style="padding:18px;border-radius:14px;background:#edf4ff;font-size:18px;font-weight:bold;">%s</div>
            <p style="margin:24px 0 0;"><a href="%s" style="display:inline-block;padding:12px 18px;border-radius:10px;color:#ffffff;background:#3f7ee8;font-weight:bold;text-decoration:none;">View saved page</a></p>
          </td></tr>
        </table>
        <p style="margin:18px 0 0;color:#8a959f;font-size:12px;">bram-html &middot; Admin notification</p>
      </td></tr>
    </table>
  </body>
</html>
`, escapedOwner, action, escapedTitle, escapedURL)
	htmlBody = strings.ReplaceAll(htmlBody, "\n", "\r\n")
	textBody := fmt.Sprintf("%s %s:\r\n\r\n%s\r\n\r\nView the saved page: %s\r\n", notification.OwnerEmail, action, notification.PageTitle, notification.PageURL)

	return multipartMessage(from, recipients, "A page was saved on bram-html", pageSavedMailBoundary, "8bit", textBody, htmlBody)
}

func multipartMessage(from string, recipients []string, subject, boundary, transferEncoding, textBody, htmlBody string) string {
	return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n"+
		"--%s\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: %s\r\n\r\n%s\r\n"+
		"--%s\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: %s\r\n\r\n%s\r\n"+
		"--%s--\r\n", from, strings.Join(recipients, ", "), subject, boundary,
		boundary, transferEncoding, textBody, boundary, transferEncoding, htmlBody, boundary)
}

func (m SMTPMailer) SendLoginCode(ctx context.Context, email, code string) error {
	return m.send(ctx, []string{email}, loginCodeMessage(m.From, email, code))
}

func (m SMTPMailer) SendPageSaved(ctx context.Context, recipients []string, notification PageSavedNotification) error {
	sendErrors := make(chan error, len(recipients))
	var sends sync.WaitGroup
	for _, recipient := range recipients {
		sends.Add(1)
		go func() {
			defer sends.Done()
			if err := m.send(ctx, []string{recipient}, pageSavedMessage(m.From, []string{recipient}, notification)); err != nil {
				sendErrors <- fmt.Errorf("send to %s: %w", recipient, err)
			}
		}()
	}
	sends.Wait()
	close(sendErrors)
	var joined []error
	for err := range sendErrors {
		joined = append(joined, err)
	}
	return errors.Join(joined...)
}

func (m SMTPMailer) send(ctx context.Context, recipients []string, message string) error {
	address := net.JoinHostPort(m.Host, m.Port)
	dialer := net.Dialer{Timeout: 10 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer connection.Close()
	deadline := time.Now().Add(15 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return err
	}
	cancelWatch := make(chan struct{})
	defer close(cancelWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.SetDeadline(time.Now())
		case <-cancelWatch:
		}
	}()

	client, err := smtp.NewClient(connection, m.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: m.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("SMTP server %s does not support STARTTLS", m.Host)
	}
	if m.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", m.Username, m.Password, m.Host)); err != nil {
			return err
		}
	}
	if err := client.Mail(m.From); err != nil {
		return err
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := io.WriteString(writer, message); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	_ = client.Quit()
	return nil
}
