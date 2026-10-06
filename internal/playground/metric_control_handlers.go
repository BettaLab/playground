package playground

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var metricControlFields = map[string]bool{
	"like_count":       true,
	"reply_count":      true,
	"retweet_count":    true,
	"quote_count":      true,
	"bookmark_count":   true,
	"impression_count": true,
}

// HandlePostMetricsControl exposes Playground-only metric replacement for tests/operators.
func HandlePostMetricsControl(state *State) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		postID := strings.TrimPrefix(r.URL.Path, "/__playground/tweets/")
		postID = strings.TrimSuffix(postID, "/metrics")
		if postID == "" || strings.Contains(postID, "/") {
			WriteError(w, http.StatusNotFound, "Post metrics endpoint not found", http.StatusNotFound)
			return
		}

		metrics, err := decodeMetricControlRequest(r)
		if err != nil {
			WriteError(w, http.StatusBadRequest, err.Error(), http.StatusBadRequest)
			return
		}

		if err := state.SetPostMetrics(postID, metrics); err != nil {
			if strings.Contains(err.Error(), "not found") {
				WriteError(w, http.StatusNotFound, err.Error(), http.StatusNotFound)
				return
			}
			WriteError(w, http.StatusBadRequest, err.Error(), http.StatusBadRequest)
			return
		}

		WriteJSONSafe(w, http.StatusOK, map[string]interface{}{
			"id":             postID,
			"public_metrics": metrics,
		})
	}
}

func decodeMetricControlRequest(r *http.Request) (TweetMetrics, error) {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var payload map[string]json.RawMessage
	if err := decoder.Decode(&payload); err != nil {
		return TweetMetrics{}, fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != nil && err != io.EOF {
		return TweetMetrics{}, fmt.Errorf("invalid JSON body: multiple JSON values")
	}

	for field := range payload {
		if !metricControlFields[field] {
			return TweetMetrics{}, fmt.Errorf("unknown metric field %q", field)
		}
	}

	likeCount, err := requiredMetric(payload, "like_count")
	if err != nil {
		return TweetMetrics{}, err
	}
	replyCount, err := requiredMetric(payload, "reply_count")
	if err != nil {
		return TweetMetrics{}, err
	}
	retweetCount, err := requiredMetric(payload, "retweet_count")
	if err != nil {
		return TweetMetrics{}, err
	}
	quoteCount, err := requiredMetric(payload, "quote_count")
	if err != nil {
		return TweetMetrics{}, err
	}
	bookmarkCount, err := optionalMetric(payload, "bookmark_count")
	if err != nil {
		return TweetMetrics{}, err
	}
	impressionCount, err := optionalMetric(payload, "impression_count")
	if err != nil {
		return TweetMetrics{}, err
	}

	metrics := TweetMetrics{
		LikeCount:       likeCount,
		ReplyCount:      replyCount,
		RetweetCount:    retweetCount,
		QuoteCount:      quoteCount,
		BookmarkCount:   bookmarkCount,
		ImpressionCount: impressionCount,
	}
	return metrics, metrics.Validate()
}

func requiredMetric(payload map[string]json.RawMessage, field string) (int, error) {
	raw, ok := payload[field]
	if !ok {
		return 0, fmt.Errorf("missing required metric field %q", field)
	}
	return decodeMetricField(field, raw)
}

func optionalMetric(payload map[string]json.RawMessage, field string) (int, error) {
	raw, ok := payload[field]
	if !ok {
		return 0, nil
	}
	return decodeMetricField(field, raw)
}

func decodeMetricField(field string, raw json.RawMessage) (int, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, fmt.Errorf("metric field %q must be an integer", field)
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("metric field %q must be an integer", field)
	}
	return value, nil
}
