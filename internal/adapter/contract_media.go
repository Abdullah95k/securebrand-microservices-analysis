package adapter

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	contentv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/content/v1"
	mediav1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/media/v1"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/topics"
)

// The contract's media hand-off: content.media.ready.v1 announces a file to
// analyse, content.media.deleted.v1 says a file is gone. Acknowledgements
// go to content.media.processed.v1 (store).

func decodeContractMedia(topic string, value []byte) ([]Event, error) {
	switch topic {
	case topics.ContentMediaReady:
		var m mediav1.MediaReady
		if err := proto.Unmarshal(value, &m); err != nil {
			return nil, err
		}
		if m.GetEventId() == "" || m.GetMediaId() == "" {
			return nil, fmt.Errorf("media ready without event_id or media_id")
		}
		return []Event{{Media: FromContractMediaReady(&m)}}, nil
	case topics.ContentMediaDeleted:
		var m mediav1.MediaDeleted
		if err := proto.Unmarshal(value, &m); err != nil {
			return nil, err
		}
		if m.GetMediaId() == "" {
			return nil, fmt.Errorf("media deleted without media_id")
		}
		return []Event{{MediaGone: &core.MediaGone{MediaID: m.GetMediaId(),
			Reason: enumName(m.GetReason().String(), "DELETE_REASON_")}}}, nil
	}
	return nil, nil
}

// FromContractMediaReady maps an announced file to a media job. Kind is the
// media's type (image, video, gif, sticker, audio, document) and Role its
// role (content, attachment, cover, thumbnail...): which pairs are analysed
// is analysis.media.contract_kinds.
func FromContractMediaReady(m *mediav1.MediaReady) *core.MediaJob {
	o := m.GetObject()
	return &core.MediaJob{
		MediaID:        m.GetMediaId(),
		OwnerContentID: m.GetOwnerId(),
		Platform:       PlatformName(m.GetPlatform()),
		SourceID:       m.GetAccountId(),
		Kind:           MediaTypeName(m.GetType()),
		Role:           MediaRoleName(m.GetRole()),
		Bucket:         o.GetBucket(),
		Key:            o.GetKey(),
		Size:           o.GetSizeBytes(),
		ContentType:    o.GetContentType(),
		Sha256:         strings.ToLower(o.GetSha256()),
		DurationMS:     m.GetDurationMs(),
		AltText:        m.GetAltText(),
		DeleteAfter:    ts(m.GetDeleteAfter()),
		Attempt:        int(m.GetAttempt()),
		AckFormat:      core.AckContract,
	}
}

// MediaTypeName is a contract media type in lower case: MEDIA_TYPE_IMAGE
// is "image".
func MediaTypeName(t contentv1.MediaType) string { return enumName(t.String(), "MEDIA_TYPE_") }

// MediaRoleName is a contract media role in lower case:
// MEDIA_ROLE_PROFILE_PICTURE is "profile_picture".
func MediaRoleName(r contentv1.MediaRole) string { return enumName(r.String(), "MEDIA_ROLE_") }

func enumName(s, prefix string) string { return strings.ToLower(strings.TrimPrefix(s, prefix)) }
