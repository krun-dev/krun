package mail

import (
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"time"

	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/localize"
)

type Sender struct{ Config appconfig.Config }

func New(config appconfig.Config) Sender { return Sender{Config: config} }
func (sender Sender) Send(ctx context.Context, to, code, language string) error {
	c := sender.Config
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	address := net.JoinHostPort(c.SMTP.Host, strconv.Itoa(c.SMTP.Port))
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer connection.Close()
	deadline, _ := ctx.Deadline()
	if err := connection.SetDeadline(deadline); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	tlsConfig := &tls.Config{ServerName: c.SMTP.Host, MinVersion: tls.VersionTLS12}
	var smtpConnection net.Conn = connection
	if c.SMTP.TLS == "tls" {
		tlsConnection := tls.Client(connection, tlsConfig)
		if err := tlsConnection.HandshakeContext(ctx); err != nil {
			return err
		}
		smtpConnection = tlsConnection
	}
	client, err := smtp.NewClient(smtpConnection, c.SMTP.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	if c.SMTP.TLS == "starttls" {
		// Require TLS rather than silently falling back to plaintext.
		if err := client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if c.SMTP.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", c.SMTP.Username, c.SMTP.Password, c.SMTP.Host)); err != nil {
			return err
		}
	}
	if err := client.Mail(c.SMTP.From); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	subject := mime.QEncoding.Encode("utf-8", localize.Text(language, "Your krun sign-in code"))
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", c.SMTP.From, to, subject, time.Now().UTC().Format(time.RFC1123Z), localize.Format(language, "Your sign-in code is %s. It expires in 10 minutes. If you did not request this code, ignore this email.", code))
	if _, err := writer.Write([]byte(message)); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	// DATA has already been accepted; a failed QUIT must not invalidate delivery.
	_ = client.Quit()
	return nil
}
