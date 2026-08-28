package ses

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/im10furry/saas/biz/notification"
	"github.com/im10furry/saas/core/types"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/smithy-go"
)

func TestSESNotifierSendEmail(t *testing.T) {
	sender := &fakeSESSender{output: &sesv2.SendEmailOutput{MessageId: stringPtr("ses-123")}}
	notifier, err := NewSESNotifier(SESConfig{
		Sender:               sender,
		From:                 "Acme <noreply@example.com>",
		ConfigurationSetName: "prod",
		TenantName:           "tenant-name",
		ReplyToAddresses:     []string{"reply@example.com"},
	})
	if err != nil {
		t.Fatalf("NewSESNotifier() error = %v", err)
	}
	message := testMessage(notification.ChannelEmail)
	message.Body = "welcome"
	message.Metadata = map[string]string{"trace.id": "internal-only"}
	message.Tags = map[string]string{"tenant": "tenant-a"}

	result, err := notifier.SendEmail(context.Background(), message)
	if err != nil {
		t.Fatalf("SendEmail() error = %v", err)
	}
	if result.MessageID != "ses-123" {
		t.Fatalf("SendEmail() MessageID = %q, want ses-123", result.MessageID)
	}

	input := sender.input
	if input == nil || input.Content == nil || input.Content.Simple == nil {
		t.Fatalf("SES input missing simple content: %#v", input)
	}
	if !strings.Contains(stringValue(input.FromEmailAddress), "noreply@example.com") ||
		stringValue(input.ConfigurationSetName) != "prod" ||
		stringValue(input.TenantName) != "tenant-name" {
		t.Fatalf("SES input metadata = from %q config %q tenant %q", stringValue(input.FromEmailAddress), stringValue(input.ConfigurationSetName), stringValue(input.TenantName))
	}
	if len(input.Destination.ToAddresses) != 1 || input.Destination.ToAddresses[0] != "user@example.com" {
		t.Fatalf("SES input recipients = %#v, want user@example.com", input.Destination.ToAddresses)
	}
	if got := stringValue(input.Content.Simple.Subject.Data); got != "Hi" {
		t.Fatalf("SES input subject = %q, want Hi", got)
	}
	if input.Content.Simple.Body == nil || input.Content.Simple.Body.Text == nil || stringValue(input.Content.Simple.Body.Text.Data) != "welcome" {
		t.Fatalf("SES input text body = %#v, want welcome", input.Content.Simple.Body)
	}
	if len(input.EmailTags) != 1 || stringValue(input.EmailTags[0].Name) != "tenant" || stringValue(input.EmailTags[0].Value) != "tenant-a" {
		t.Fatalf("SES input tags = %#v, want tenant tag", input.EmailTags)
	}
	if len(input.ReplyToAddresses) != 1 || input.ReplyToAddresses[0] != "reply@example.com" {
		t.Fatalf("SES input reply-to = %#v, want reply@example.com", input.ReplyToAddresses)
	}
}

func TestSESNotifierEncodesFriendlyFromName(t *testing.T) {
	sender := &fakeSESSender{output: &sesv2.SendEmailOutput{MessageId: stringPtr("ses-123")}}
	notifier, err := NewSESNotifier(SESConfig{Sender: sender, From: "Jos\u00e9 <noreply@example.com>"})
	if err != nil {
		t.Fatalf("NewSESNotifier() error = %v", err)
	}
	message := testMessage(notification.ChannelEmail)
	message.Body = "welcome"

	if _, err := notifier.SendEmail(context.Background(), message); err != nil {
		t.Fatalf("SendEmail() error = %v", err)
	}
	from := stringValue(sender.input.FromEmailAddress)
	if strings.Contains(from, "Jos\u00e9") || !strings.Contains(from, "noreply@example.com") {
		t.Fatalf("SES from = %q, want encoded friendly name with ASCII address", from)
	}
}

func TestSESNotifierHTMLBody(t *testing.T) {
	sender := &fakeSESSender{output: &sesv2.SendEmailOutput{MessageId: stringPtr("ses-123")}}
	notifier, err := NewSESNotifier(SESConfig{Sender: sender, From: "noreply@example.com", BodyFormat: SESBodyHTML})
	if err != nil {
		t.Fatalf("NewSESNotifier() error = %v", err)
	}
	message := testMessage(notification.ChannelEmail)
	message.Body = "<p>welcome</p>"
	if err := notifier.Send(context.Background(), message); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if sender.input.Content.Simple.Body == nil || sender.input.Content.Simple.Body.Html == nil ||
		stringValue(sender.input.Content.Simple.Body.Html.Data) != "<p>welcome</p>" {
		t.Fatalf("SES input html body = %#v, want html body", sender.input.Content.Simple.Body)
	}
}

func TestSESNotifierValidation(t *testing.T) {
	if _, err := NewSESNotifier(SESConfig{}); !errors.Is(err, ErrInvalidSESConfig) {
		t.Fatalf("NewSESNotifier(empty) error = %v, want ErrInvalidSESConfig", err)
	}
	if _, err := NewSESNotifier(SESConfig{Sender: &fakeSESSender{}, From: "bad\r\nBcc: x@example.com"}); !errors.Is(err, ErrInvalidSESConfig) {
		t.Fatalf("NewSESNotifier(injected from) error = %v, want ErrInvalidSESConfig", err)
	}
	if _, err := NewSESNotifier(SESConfig{Sender: &fakeSESSender{}, From: "user@ex\u00e4mple.com"}); !errors.Is(err, ErrInvalidSESConfig) {
		t.Fatalf("NewSESNotifier(non-ascii from) error = %v, want ErrInvalidSESConfig", err)
	}
	if _, err := NewSESNotifier(SESConfig{Sender: &fakeSESSender{}, From: "noreply@example.com", ReplyToAddresses: []string{"reply@ex\u00e4mple.com"}}); !errors.Is(err, ErrInvalidSESConfig) {
		t.Fatalf("NewSESNotifier(non-ascii reply-to) error = %v, want ErrInvalidSESConfig", err)
	}
	if _, err := NewSESNotifier(SESConfig{Sender: &fakeSESSender{}, From: "noreply@example.com", BodyFormat: "raw"}); !errors.Is(err, ErrInvalidSESConfig) {
		t.Fatalf("NewSESNotifier(bad body format) error = %v, want ErrInvalidSESConfig", err)
	}
	if err := (*SESNotifier)(nil).Send(context.Background(), testMessage(notification.ChannelEmail)); !errors.Is(err, notification.ErrNilNotifier) {
		t.Fatalf("nil Send() error = %v, want notification.ErrNilNotifier", err)
	}
}

func TestSESNotifierMessageValidation(t *testing.T) {
	notifier, err := NewSESNotifier(SESConfig{Sender: &fakeSESSender{}, From: "noreply@example.com"})
	if err != nil {
		t.Fatalf("NewSESNotifier() error = %v", err)
	}
	message := testMessage("sms")
	message.Body = "body"
	if _, err := notifier.SendEmail(context.Background(), message); !errors.Is(err, notification.ErrUnsupportedChannel) {
		t.Fatalf("SendEmail(wrong channel) error = %v, want notification.ErrUnsupportedChannel", err)
	}
	message = testMessage(notification.ChannelEmail)
	message.Body = ""
	if _, err := notifier.SendEmail(context.Background(), message); !errors.Is(err, notification.ErrInvalidMessage) {
		t.Fatalf("SendEmail(empty body) error = %v, want notification.ErrInvalidMessage", err)
	}
	message.Body = "body"
	message.Tags = map[string]string{"bad key": "value"}
	if _, err := notifier.SendEmail(context.Background(), message); !errors.Is(err, notification.ErrInvalidMessage) {
		t.Fatalf("SendEmail(bad tags) error = %v, want notification.ErrInvalidMessage", err)
	}
	message.Tags = nil
	message.To = "user@ex\u00e4mple.com"
	if _, err := notifier.SendEmail(context.Background(), message); !errors.Is(err, notification.ErrInvalidMessage) {
		t.Fatalf("SendEmail(non-ascii recipient) error = %v, want notification.ErrInvalidMessage", err)
	}
}

func TestSESNotifierDoesNotMapMetadataToTags(t *testing.T) {
	sender := &fakeSESSender{output: &sesv2.SendEmailOutput{MessageId: stringPtr("ses-123")}}
	notifier, err := NewSESNotifier(SESConfig{Sender: sender, From: "noreply@example.com"})
	if err != nil {
		t.Fatalf("NewSESNotifier() error = %v", err)
	}
	message := testMessage(notification.ChannelEmail)
	message.Body = "welcome"
	message.Metadata = map[string]string{"trace.id": "internal-only"}

	if _, err := notifier.SendEmail(context.Background(), message); err != nil {
		t.Fatalf("SendEmail() error = %v", err)
	}
	if len(sender.input.EmailTags) != 0 {
		t.Fatalf("SES input tags = %#v, want absent for metadata-only message", sender.input.EmailTags)
	}
}

func TestSESNotifierRejectsEmptyProviderID(t *testing.T) {
	notifier, err := NewSESNotifier(SESConfig{Sender: &fakeSESSender{output: &sesv2.SendEmailOutput{}}, From: "noreply@example.com"})
	if err != nil {
		t.Fatalf("NewSESNotifier() error = %v", err)
	}
	message := testMessage(notification.ChannelEmail)
	message.Body = "body"

	_, err = notifier.SendEmail(context.Background(), message)
	if !errors.Is(err, ErrSESDelivery) {
		t.Fatalf("SendEmail(empty id) error = %v, want ErrSESDelivery", err)
	}
	var deliveryErr *SESDeliveryError
	if !errors.As(err, &deliveryErr) || deliveryErr.Code != "MissingMessageID" || !deliveryErr.Retryable() {
		t.Fatalf("SES delivery error = %#v, want retryable MissingMessageID", deliveryErr)
	}
}

func TestSESNotifierClassifiesErrors(t *testing.T) {
	for _, tt := range []struct {
		name      string
		err       error
		retryable bool
	}{
		{
			name:      "client",
			err:       &smithy.GenericAPIError{Code: "MessageRejected", Message: "bad address", Fault: smithy.FaultClient},
			retryable: false,
		},
		{
			name:      "throttle",
			err:       &smithy.GenericAPIError{Code: "TooManyRequestsException", Message: "rate limit", Fault: smithy.FaultClient},
			retryable: true,
		},
		{
			name:      "server",
			err:       &smithy.GenericAPIError{Code: "InternalFailure", Message: "down", Fault: smithy.FaultServer},
			retryable: true,
		},
		{
			name:      "transport",
			err:       errors.New("connection reset"),
			retryable: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			notifier, err := NewSESNotifier(SESConfig{Sender: &fakeSESSender{err: tt.err}, From: "noreply@example.com"})
			if err != nil {
				t.Fatalf("NewSESNotifier() error = %v", err)
			}
			message := testMessage(notification.ChannelEmail)
			message.Body = "body"
			_, err = notifier.SendEmail(context.Background(), message)
			if !errors.Is(err, ErrSESDelivery) {
				t.Fatalf("SendEmail() error = %v, want ErrSESDelivery", err)
			}
			var deliveryErr *SESDeliveryError
			if !errors.As(err, &deliveryErr) {
				t.Fatalf("SendEmail() error = %v, want SESDeliveryError", err)
			}
			if deliveryErr.Retryable() != tt.retryable || notification.DefaultRetryIf(err) != tt.retryable {
				t.Fatalf("retryable = %v default = %v, want %v", deliveryErr.Retryable(), notification.DefaultRetryIf(err), tt.retryable)
			}
			if strings.Contains(err.Error(), "bad address") || strings.Contains(err.Error(), "rate limit") {
				t.Fatalf("error string %q leaked provider message", err.Error())
			}
		})
	}
}

func TestSESNotifierPreservesContextErrors(t *testing.T) {
	notifier, err := NewSESNotifier(SESConfig{Sender: &fakeSESSender{err: context.Canceled}, From: "noreply@example.com"})
	if err != nil {
		t.Fatalf("NewSESNotifier() error = %v", err)
	}
	message := testMessage(notification.ChannelEmail)
	message.Body = "body"
	_, err = notifier.SendEmail(context.Background(), message)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SendEmail() error = %v, want context.Canceled", err)
	}
}

type fakeSESSender struct {
	input  *sesv2.SendEmailInput
	output *sesv2.SendEmailOutput
	err    error
}

func (sender *fakeSESSender) SendEmail(ctx context.Context, params *sesv2.SendEmailInput, optFns ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sender.input = params
	if sender.err != nil {
		return nil, sender.err
	}
	if sender.output != nil {
		return sender.output, nil
	}
	return &sesv2.SendEmailOutput{}, nil
}

func testMessage(channel string) notification.Message {
	return notification.Message{
		TenantID: types.TenantID("tenant-a"),
		Channel:  channel,
		To:       "user@example.com",
		Subject:  "Hi",
	}
}
