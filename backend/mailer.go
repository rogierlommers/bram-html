package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/smtp"
	"time"
)

type Mailer interface {
	SendMagicLink(ctx context.Context, email, link string) error
}

type LogMailer struct {
	Logger *log.Logger
}

func (m LogMailer) SendMagicLink(_ context.Context, email, link string) error {
	m.Logger.Printf("development magic link for %s: %s", email, link)
	return nil
}

type SMTPMailer struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
}

func (m SMTPMailer) SendMagicLink(ctx context.Context, email, link string) error {
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
	if err := client.Rcpt(email); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: Sign in to bram-html\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nOpen this one-time link to sign in to bram-html:\r\n\r\n%s\r\n", m.From, email, link)
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
