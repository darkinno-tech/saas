package ses

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/darkinno-tech/saas/biz/notification"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"
)

const (
	defaultSESCharset       = "UTF-8"
	defaultSESTagNameLimit  = 256
	defaultSESTagValueLimit = 256
)

// SESBodyFormat controls how notification.Message.Body is mapped to Amazon SES.
type SESBodyFormat string

const (
	// SESBodyText sends notification.Message.Body as the text body.
	SESBodyText SESBodyFormat = "text"

	// SESBodyHTML sends notification.Message.Body as the HTML body.
	SESBodyHTML SESBodyFormat = "html"
)

// SESSender is implemented by *sesv2.Client.
type SESSender interface {
	SendEmail(ctx context.Context, params *sesv2.SendEmailInput, optFns ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

// SESConfig configures SESNotifier.
type SESConfig struct {
	Sender               SESSender
	From                 string
	Channel              string
	BodyFormat           SESBodyFormat
	Charset              string
	ConfigurationSetName string
	TenantName           string
	ReplyToAddresses     []string
}

// SESResult is the successful Amazon SES SendEmail response.
type SESResult struct {
	MessageID string
}

// SESDeliveryError describes a failed Amazon SES send.
type SESDeliveryError struct {
	Code  string
	Fault string
	Retry bool
}

// Error returns a safe delivery error without embedding provider response text.
func (err *SESDeliveryError) Error() string {
	if err == nil || err.Code == "" {
		return ErrSESDelivery.Error()
	}
	return fmt.Sprintf("%s: %s", ErrSESDelivery, err.Code)
}

// Unwrap returns the sentinel SES delivery error.
func (err *SESDeliveryError) Unwrap() error {
	return ErrSESDelivery
}

// Retryable reports whether the SES failure is normally safe to retry.
func (err *SESDeliveryError) RetryableError() bool {
	return err != nil && err.Retry
}

// Retryable reports whether the SES failure is normally safe to retry.
func (err *SESDeliveryError) Retryable() bool {
	return err != nil && err.Retry
}

// SESNotifier sends email notifications through Amazon SES API v2.
type SESNotifier struct {
	sender               SESSender
	from                 string
	channel              string
	bodyFormat           SESBodyFormat
	charset              string
	configurationSetName string
	tenantName           string
	replyToAddresses     []string
}

var _ notification.Notifier = (*SESNotifier)(nil)

// NewSESNotifier creates an Amazon SES-backed email notifier.
func NewSESNotifier(config SESConfig) (*SESNotifier, error) {
	config = normalizeSESConfig(config)
	if err := validateSESConfig(config); err != nil {
		return nil, err
	}

	from, err := formatSESAddress(config.From)
	if err != nil {
		return nil, ErrInvalidSESConfig
	}
	replyTo, err := formatSESAddresses(config.ReplyToAddresses)
	if err != nil {
		return nil, ErrInvalidSESConfig
	}
	return &SESNotifier{
		sender:               config.Sender,
		from:                 from,
		channel:              config.Channel,
		bodyFormat:           config.BodyFormat,
		charset:              config.Charset,
		configurationSetName: config.ConfigurationSetName,
		tenantName:           config.TenantName,
		replyToAddresses:     replyTo,
	}, nil
}

// Send sends message through Amazon SES and discards the returned provider ID.
func (notifier *SESNotifier) Send(ctx context.Context, message notification.Message) error {
	_, err := notifier.SendEmail(ctx, message)
	return err
}

// SendEmail sends message through Amazon SES and returns the provider ID.
func (notifier *SESNotifier) SendEmail(ctx context.Context, message notification.Message) (SESResult, error) {
	if err := ctx.Err(); err != nil {
		return SESResult{}, err
	}
	if notifier == nil {
		return SESResult{}, notification.ErrNilNotifier
	}
	if err := validateSESMessage(notifier.channel, message); err != nil {
		return SESResult{}, err
	}

	input, err := notifier.input(message.Clone())
	if err != nil {
		return SESResult{}, err
	}
	output, err := notifier.sender.SendEmail(ctx, input)
	if err != nil {
		return SESResult{}, classifySESError(err)
	}
	if output == nil {
		return SESResult{}, &SESDeliveryError{Retry: true}
	}
	messageID := stringValue(output.MessageId)
	if strings.TrimSpace(messageID) == "" {
		return SESResult{}, &SESDeliveryError{Code: "MissingMessageID", Retry: true}
	}
	return SESResult{MessageID: messageID}, nil
}

func (notifier *SESNotifier) input(message notification.Message) (*sesv2.SendEmailInput, error) {
	recipients, err := parseAddressList(message.To)
	if err != nil {
		return nil, notification.ErrInvalidMessage
	}
	tags, err := sesTags(message.Tags)
	if err != nil {
		return nil, err
	}

	input := &sesv2.SendEmailInput{
		Content: &types.EmailContent{
			Simple: &types.Message{
				Subject: &types.Content{Data: stringPtr(message.Subject), Charset: stringPtr(notifier.charset)},
				Body:    notifier.body(message.Body),
			},
		},
		Destination:      &types.Destination{ToAddresses: addressStrings(recipients)},
		EmailTags:        tags,
		FromEmailAddress: stringPtr(notifier.from),
		ReplyToAddresses: append([]string(nil), notifier.replyToAddresses...),
	}
	if notifier.configurationSetName != "" {
		input.ConfigurationSetName = stringPtr(notifier.configurationSetName)
	}
	if notifier.tenantName != "" {
		input.TenantName = stringPtr(notifier.tenantName)
	}
	return input, nil
}

func (notifier *SESNotifier) body(body string) *types.Body {
	content := &types.Content{Data: stringPtr(body), Charset: stringPtr(notifier.charset)}
	switch notifier.bodyFormat {
	case SESBodyHTML:
		return &types.Body{Html: content}
	default:
		return &types.Body{Text: content}
	}
}

func normalizeSESConfig(config SESConfig) SESConfig {
	config.From = strings.TrimSpace(config.From)
	config.Channel = strings.TrimSpace(config.Channel)
	config.Charset = strings.TrimSpace(config.Charset)
	config.ConfigurationSetName = strings.TrimSpace(config.ConfigurationSetName)
	config.TenantName = strings.TrimSpace(config.TenantName)
	if config.Channel == "" {
		config.Channel = notification.ChannelEmail
	}
	if config.BodyFormat == "" {
		config.BodyFormat = SESBodyText
	}
	if config.Charset == "" {
		config.Charset = defaultSESCharset
	}
	return config
}

func validateSESConfig(config SESConfig) error {
	if config.Sender == nil || config.From == "" || config.Channel == "" || config.Charset == "" {
		return ErrInvalidSESConfig
	}
	if hasHeaderInjection(config.From) || hasHeaderInjection(config.Channel) || hasHeaderInjection(config.Charset) ||
		hasHeaderInjection(config.ConfigurationSetName) || hasHeaderInjection(config.TenantName) {
		return ErrInvalidSESConfig
	}
	if _, err := formatSESAddress(config.From); err != nil {
		return ErrInvalidSESConfig
	}
	for _, replyTo := range config.ReplyToAddresses {
		if hasHeaderInjection(replyTo) {
			return ErrInvalidSESConfig
		}
		if _, err := formatSESAddress(replyTo); err != nil {
			return ErrInvalidSESConfig
		}
	}
	switch config.BodyFormat {
	case SESBodyText, SESBodyHTML:
	default:
		return ErrInvalidSESConfig
	}
	return nil
}

func validateSESMessage(channel string, message notification.Message) error {
	if err := message.Validate(); err != nil {
		return err
	}
	if message.Channel != channel {
		return notification.ErrUnsupportedChannel
	}
	if strings.TrimSpace(message.Subject) == "" || strings.TrimSpace(message.Body) == "" {
		return notification.ErrInvalidMessage
	}
	if hasHeaderInjection(message.To) || hasHeaderInjection(message.Subject) {
		return notification.ErrInvalidMessage
	}
	recipients, err := parseAddressList(message.To)
	if err != nil || !validSESAddresses(recipients) {
		return notification.ErrInvalidMessage
	}
	if _, err := sesTags(message.Tags); err != nil {
		return err
	}
	return nil
}

func sesTags(metadata map[string]string) ([]types.MessageTag, error) {
	if len(metadata) == 0 {
		return nil, nil
	}

	tags := make([]types.MessageTag, 0, len(metadata))
	for key, value := range metadata {
		if !validTagValue(key, defaultSESTagNameLimit) || !validTagValue(value, defaultSESTagValueLimit) {
			return nil, notification.ErrInvalidMessage
		}
		tags = append(tags, types.MessageTag{Name: stringPtr(key), Value: stringPtr(value)})
	}
	return tags, nil
}

func classifySESError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return &SESDeliveryError{
			Code:  apiErr.ErrorCode(),
			Fault: apiErr.ErrorFault().String(),
			Retry: apiErr.ErrorFault() == smithy.FaultServer || isSESThrottle(apiErr.ErrorCode()),
		}
	}
	return &SESDeliveryError{Retry: true}
}

func isSESThrottle(code string) bool {
	code = strings.ToLower(code)
	return strings.Contains(code, "throttl") ||
		strings.Contains(code, "rate") ||
		strings.Contains(code, "limit") ||
		strings.Contains(code, "toomanyrequest")
}

func formatSESAddress(value string) (string, error) {
	address, err := parseSingleAddress(value)
	if err != nil || !validSESAddress(address) {
		return "", notification.ErrInvalidMessage
	}
	if address.Name == "" {
		return address.Address, nil
	}
	return address.String(), nil
}

func formatSESAddresses(values []string) ([]string, error) {
	formatted := make([]string, len(values))
	for i, value := range values {
		address, err := formatSESAddress(value)
		if err != nil {
			return nil, err
		}
		formatted[i] = address
	}
	return formatted, nil
}

func validSESAddresses(addresses []*mail.Address) bool {
	for _, address := range addresses {
		if !validSESAddress(address) {
			return false
		}
	}
	return true
}

func validSESAddress(address *mail.Address) bool {
	return address != nil && isASCII(address.Address)
}

func isASCII(value string) bool {
	for _, r := range value {
		if r > 127 {
			return false
		}
	}
	return true
}

func stringPtr(value string) *string {
	return &value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func parseSingleAddress(value string) (*mail.Address, error) {
	address, err := mail.ParseAddress(value)
	if err != nil {
		return nil, err
	}
	if address.Address == "" {
		return nil, notification.ErrInvalidMessage
	}
	return address, nil
}

func parseAddressList(value string) ([]*mail.Address, error) {
	addresses, err := mail.ParseAddressList(value)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, notification.ErrInvalidMessage
	}
	return addresses, nil
}

func addressStrings(addresses []*mail.Address) []string {
	values := make([]string, len(addresses))
	for i, address := range addresses {
		if address.Name == "" {
			values[i] = address.Address
			continue
		}
		values[i] = address.String()
	}
	return values
}

func hasHeaderInjection(value string) bool {
	return strings.ContainsAny(value, "\r\n")
}

func validTagValue(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}
