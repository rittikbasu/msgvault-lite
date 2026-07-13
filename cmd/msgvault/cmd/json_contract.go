package cmd

import (
	"encoding/json"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

const (
	jsonSchemaVersion    = 1
	maxJSONPageSize      = 200
	defaultJSONBodyBytes = 64 * 1024
	maxJSONBodyBytes     = 1024 * 1024
)

type jsonPage struct {
	Limit    int    `json:"limit"`
	Offset   int    `json:"offset"`
	Returned int    `json:"returned"`
	Total    *int64 `json:"total,omitempty"`
	HasMore  bool   `json:"has_more"`
}

type jsonListResponse struct {
	SchemaVersion int                  `json:"schema_version"`
	Items         []jsonMessageSummary `json:"items"`
	Page          jsonPage             `json:"page"`
}

type jsonMessageSummary struct {
	ID                   int64      `json:"id"`
	SourceMessageID      string     `json:"source_message_id"`
	ConversationID       int64      `json:"conversation_id"`
	SourceConversationID string     `json:"source_conversation_id"`
	Subject              string     `json:"subject"`
	Snippet              string     `json:"snippet"`
	FromEmail            string     `json:"from_email"`
	FromName             string     `json:"from_name"`
	SentAt               string     `json:"sent_at,omitempty"`
	SizeEstimate         int64      `json:"size_estimate"`
	HasAttachments       bool       `json:"has_attachments"`
	Labels               []string   `json:"labels"`
	DeletedFromSourceAt  *time.Time `json:"deleted_from_source_at,omitempty"`
}

type jsonStatusResponse struct {
	SchemaVersion int               `json:"schema_version"`
	Database      string            `json:"database"`
	Messages      jsonMessageCounts `json:"messages"`
	Threads       int64             `json:"threads"`
	Attachments   int64             `json:"attachments"`
	Labels        int64             `json:"labels"`
	Accounts      int64             `json:"accounts"`
	DatabaseBytes int64             `json:"database_bytes"`
}

type jsonMessageCounts struct {
	Total             int64 `json:"total"`
	Active            int64 `json:"active"`
	DeletedFromSource int64 `json:"deleted_from_source"`
}

type jsonShowResponse struct {
	SchemaVersion int               `json:"schema_version"`
	Message       jsonMessageDetail `json:"message"`
}

type jsonMessageDetail struct {
	ID                   int64            `json:"id"`
	SourceMessageID      string           `json:"source_message_id"`
	ConversationID       int64            `json:"conversation_id"`
	SourceConversationID string           `json:"source_conversation_id"`
	Subject              string           `json:"subject"`
	Snippet              string           `json:"snippet"`
	SentAt               string           `json:"sent_at,omitempty"`
	ReceivedAt           *time.Time       `json:"received_at,omitempty"`
	DeletedFromSourceAt  *time.Time       `json:"deleted_from_source_at,omitempty"`
	SizeEstimate         int64            `json:"size_estimate"`
	HasAttachments       bool             `json:"has_attachments"`
	From                 []jsonAddress    `json:"from"`
	To                   []jsonAddress    `json:"to"`
	Cc                   []jsonAddress    `json:"cc"`
	Bcc                  []jsonAddress    `json:"bcc"`
	Labels               []string         `json:"labels"`
	Attachments          []jsonAttachment `json:"attachments"`
	BodyText             string           `json:"body_text"`
	BodyTextTruncated    bool             `json:"body_text_truncated"`
	BodyHTML             string           `json:"body_html"`
	BodyHTMLTruncated    bool             `json:"body_html_truncated"`
}

type jsonAddress struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

type jsonAttachment struct {
	ID          int64  `json:"id"`
	Filename    string `json:"filename"`
	MimeType    string `json:"mime_type"`
	Size        int64  `json:"size"`
	ContentHash string `json:"content_hash"`
	URL         string `json:"url,omitempty"`
}

func writeJSON(out io.Writer, value any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func jsonTimestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func apiMessageSummary(msg store.APIMessage) jsonMessageSummary {
	return jsonMessageSummary{
		ID:                   msg.ID,
		SourceMessageID:      msg.SourceMessageID,
		ConversationID:       msg.ConversationID,
		SourceConversationID: msg.SourceConversationID,
		Subject:              msg.Subject,
		Snippet:              msg.Snippet,
		FromEmail:            msg.FromEmail,
		FromName:             msg.FromName,
		SentAt:               jsonTimestamp(msg.SentAt),
		SizeEstimate:         msg.SizeEstimate,
		HasAttachments:       msg.HasAttachments,
		Labels:               nonNilStrings(msg.Labels),
		DeletedFromSourceAt:  msg.DeletedAt,
	}
}

func queryMessageSummary(msg query.MessageSummary) jsonMessageSummary {
	return jsonMessageSummary{
		ID:                   msg.ID,
		SourceMessageID:      msg.SourceMessageID,
		ConversationID:       msg.ConversationID,
		SourceConversationID: msg.SourceConversationID,
		Subject:              msg.Subject,
		Snippet:              msg.Snippet,
		FromEmail:            msg.FromEmail,
		FromName:             msg.FromName,
		SentAt:               jsonTimestamp(msg.SentAt),
		SizeEstimate:         msg.SizeEstimate,
		HasAttachments:       msg.HasAttachments,
		Labels:               nonNilStrings(msg.Labels),
		DeletedFromSourceAt:  msg.DeletedAt,
	}
}

func boundedUTF8(value string, maxBytes int) (string, bool) {
	value = strings.ToValidUTF8(value, "�")
	if maxBytes <= 0 {
		return "", value != ""
	}
	if len(value) <= maxBytes {
		return value, false
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end], true
}

func jsonMessageDetailFrom(msg *query.MessageDetail, maxBodyBytes int) jsonMessageDetail {
	from := make([]jsonAddress, len(msg.From))
	for i, address := range msg.From {
		from[i] = jsonAddress{Email: address.Email, Name: address.Name}
	}
	to := make([]jsonAddress, len(msg.To))
	for i, address := range msg.To {
		to[i] = jsonAddress{Email: address.Email, Name: address.Name}
	}
	cc := make([]jsonAddress, len(msg.Cc))
	for i, address := range msg.Cc {
		cc[i] = jsonAddress{Email: address.Email, Name: address.Name}
	}
	bcc := make([]jsonAddress, len(msg.Bcc))
	for i, address := range msg.Bcc {
		bcc[i] = jsonAddress{Email: address.Email, Name: address.Name}
	}
	attachments := make([]jsonAttachment, len(msg.Attachments))
	for i, attachment := range msg.Attachments {
		attachments[i] = jsonAttachment{
			ID:          attachment.ID,
			Filename:    attachment.Filename,
			MimeType:    attachment.MimeType,
			Size:        attachment.Size,
			ContentHash: attachment.ContentHash,
			URL:         attachment.URL,
		}
	}
	bodyText, bodyTextTruncated := boundedUTF8(msg.BodyText, maxBodyBytes)
	bodyHTML, bodyHTMLTruncated := boundedUTF8(msg.BodyHTML, maxBodyBytes)
	return jsonMessageDetail{
		ID:                   msg.ID,
		SourceMessageID:      msg.SourceMessageID,
		ConversationID:       msg.ConversationID,
		SourceConversationID: msg.SourceConversationID,
		Subject:              msg.Subject,
		Snippet:              msg.Snippet,
		SentAt:               jsonTimestamp(msg.SentAt),
		ReceivedAt:           msg.ReceivedAt,
		DeletedFromSourceAt:  msg.DeletedAt,
		SizeEstimate:         msg.SizeEstimate,
		HasAttachments:       msg.HasAttachments,
		From:                 from,
		To:                   to,
		Cc:                   cc,
		Bcc:                  bcc,
		Labels:               nonNilStrings(msg.Labels),
		Attachments:          attachments,
		BodyText:             bodyText,
		BodyTextTruncated:    bodyTextTruncated,
		BodyHTML:             bodyHTML,
		BodyHTMLTruncated:    bodyHTMLTruncated,
	}
}
