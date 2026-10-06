package playground

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func postMetricControl(t *testing.T, state *State, postID string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/__playground/tweets/"+postID+"/metrics", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	HandlePostMetricsControl(state).ServeHTTP(recorder, req)
	return recorder
}

func metricControlJSON(t *testing.T, metrics TweetMetrics) string {
	t.Helper()
	payload := map[string]int{
		"like_count":       metrics.LikeCount,
		"reply_count":      metrics.ReplyCount,
		"retweet_count":    metrics.RetweetCount,
		"quote_count":      metrics.QuoteCount,
		"bookmark_count":   metrics.BookmarkCount,
		"impression_count": metrics.ImpressionCount,
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	return string(data)
}

func TestMetricControlSuccessfulMutation(t *testing.T) {
	state := newMetricTestState(t)
	user := state.GetDefaultUser()
	tweet := state.CreateTweet("control metrics", user.ID)
	expected := TweetMetrics{LikeCount: 25, ReplyCount: 3, RetweetCount: 7, QuoteCount: 1, BookmarkCount: 2, ImpressionCount: 450}

	recorder := postMetricControl(t, state, tweet.ID, metricControlJSON(t, expected))

	require.Equal(t, http.StatusOK, recorder.Code)
	var response map[string]interface{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, tweet.ID, response["id"])
	metricsBytes, err := json.Marshal(response["public_metrics"])
	require.NoError(t, err)
	var actual TweetMetrics
	require.NoError(t, json.Unmarshal(metricsBytes, &actual))
	require.Equal(t, expected, actual)
	require.Equal(t, expected, state.GetTweet(tweet.ID).PublicMetrics)
}

func TestMetricControlMutationIsVisibleThroughTweetLookup(t *testing.T) {
	state := newMetricTestState(t)
	user := state.GetDefaultUser()
	tweet := state.CreateTweet("control lookup", user.ID)
	expected := TweetMetrics{LikeCount: 30, ReplyCount: 4, RetweetCount: 8, QuoteCount: 2, BookmarkCount: 6, ImpressionCount: 900}

	recorder := postMetricControl(t, state, tweet.ID, metricControlJSON(t, expected))
	require.Equal(t, http.StatusOK, recorder.Code)

	response := lookupTweetsByIDs(t, state, tweet.ID)
	data := responseData(t, response)
	require.Len(t, data, 1)
	require.Equal(t, expected, metricsFromItem(t, data[0]))
}

func TestMetricControlUnknownPostReturns404AndCreatesNothing(t *testing.T) {
	state := newMetricTestState(t)
	before := len(state.GetAllTweets())

	recorder := postMetricControl(t, state, "999999999999999997", metricControlJSON(t, TweetMetrics{LikeCount: 1}))

	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.Len(t, state.GetAllTweets(), before)
}

func TestMetricControlInvalidPayloadsDoNotMutate(t *testing.T) {
	state := newMetricTestState(t)
	user := state.GetDefaultUser()
	tweet := state.CreateTweet("invalid control metrics", user.ID)
	valid := TweetMetrics{LikeCount: 4, ReplyCount: 3, RetweetCount: 2, QuoteCount: 1, BookmarkCount: 5, ImpressionCount: 100}
	require.Equal(t, http.StatusOK, postMetricControl(t, state, tweet.ID, metricControlJSON(t, valid)).Code)

	cases := map[string]string{
		"malformed JSON":              `{`,
		"missing required core field": `{"like_count":1,"reply_count":1,"retweet_count":1}`,
		"negative value":              `{"like_count":1,"reply_count":1,"retweet_count":-1,"quote_count":1}`,
		"wrong type":                  `{"like_count":"1","reply_count":1,"retweet_count":1,"quote_count":1}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			recorder := postMetricControl(t, state, tweet.ID, body)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Equal(t, valid, state.GetTweet(tweet.ID).PublicMetrics)
		})
	}
}

func TestMetricControlUsesReplacementSemantics(t *testing.T) {
	state := newMetricTestState(t)
	user := state.GetDefaultUser()
	tweet := state.CreateTweet("control replacement", user.ID)
	first := TweetMetrics{LikeCount: 10, ReplyCount: 2, RetweetCount: 3, QuoteCount: 1, BookmarkCount: 4, ImpressionCount: 100}
	second := TweetMetrics{LikeCount: 25, ReplyCount: 7, RetweetCount: 8, QuoteCount: 3, BookmarkCount: 9, ImpressionCount: 200}

	require.Equal(t, http.StatusOK, postMetricControl(t, state, tweet.ID, metricControlJSON(t, first)).Code)
	require.Equal(t, http.StatusOK, postMetricControl(t, state, tweet.ID, metricControlJSON(t, second)).Code)

	require.Equal(t, second, state.GetTweet(tweet.ID).PublicMetrics)
}

func TestMetricControlEmitsNoFilteredStreamEvent(t *testing.T) {
	state := newMetricTestState(t)
	ruleTerm := fmt.Sprintf("metriccontrol%d", time.Now().UnixNano())
	state.CreateSearchStreamRule(ruleTerm, "metric_control")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/2/tweets/search/stream?delay_ms=10", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		streamSearchTweets(recorder, req, state, &QueryParams{TweetFields: []string{"id", "text"}}, nil, "0", "/2/tweets/search/stream", http.MethodGet)
	}()

	time.Sleep(25 * time.Millisecond)
	user := state.GetDefaultUser()
	tweet := state.CreateTweet("original "+ruleTerm, user.ID)
	time.Sleep(80 * time.Millisecond)

	require.Equal(t, http.StatusOK, postMetricControl(t, state, tweet.ID, metricControlJSON(t, TweetMetrics{LikeCount: 77, ReplyCount: 6, RetweetCount: 5, QuoteCount: 4})).Code)
	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done

	body := recorder.Body.String()
	require.Equal(t, 1, strings.Count(body, "\"data\""), body)
	require.Contains(t, body, tweet.ID)
}
