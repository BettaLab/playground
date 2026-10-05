package playground

import (
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

func newMetricTestState(t *testing.T) *State {
	t.Helper()
	config, err := LoadDefaultPlaygroundConfig()
	require.NoError(t, err)
	state := NewStateWithConfig(config)
	require.NotNil(t, state.GetDefaultUser())
	return state
}

func lookupTweetsByIDs(t *testing.T, state *State, ids ...string) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/2/tweets?ids="+strings.Join(ids, ",")+"&tweet.fields=public_metrics", nil)
	data, status := handleStatefulOperation(nil, "/2/tweets", http.MethodGet, req, state, nil, &QueryParams{TweetFields: []string{"public_metrics"}}, nil)
	require.Equal(t, http.StatusOK, status)

	var response map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &response))
	return response
}

func responseData(t *testing.T, response map[string]interface{}) []interface{} {
	t.Helper()
	data, ok := response["data"].([]interface{})
	require.True(t, ok)
	return data
}

func metricsFromItem(t *testing.T, item interface{}) TweetMetrics {
	t.Helper()
	itemMap, ok := item.(map[string]interface{})
	require.True(t, ok)
	metricsValue, ok := itemMap["public_metrics"]
	require.True(t, ok)
	metricsBytes, err := json.Marshal(metricsValue)
	require.NoError(t, err)
	var metrics TweetMetrics
	require.NoError(t, json.Unmarshal(metricsBytes, &metrics))
	return metrics
}

func TestNewPostHasDeterministicPublicMetrics(t *testing.T) {
	state := newMetricTestState(t)
	user := state.GetDefaultUser()

	tweet := state.CreateTweet("deterministic metric baseline", user.ID)

	require.Equal(t, TweetMetrics{}, tweet.PublicMetrics)
	require.NoError(t, tweet.PublicMetrics.Validate())
}

func TestLookupReturnsPublicMetrics(t *testing.T) {
	state := newMetricTestState(t)
	user := state.GetDefaultUser()
	tweet := state.CreateTweet("lookup metric baseline", user.ID)

	response := lookupTweetsByIDs(t, state, tweet.ID)
	data := responseData(t, response)
	require.Len(t, data, 1)
	require.Equal(t, TweetMetrics{}, metricsFromItem(t, data[0]))
}

func TestSetPostMetricsIsVisibleThroughLookup(t *testing.T) {
	state := newMetricTestState(t)
	user := state.GetDefaultUser()
	tweet := state.CreateTweet("mutable metrics", user.ID)
	newMetrics := TweetMetrics{LikeCount: 20, ReplyCount: 2, RetweetCount: 4, QuoteCount: 1, BookmarkCount: 3, ImpressionCount: 500}

	require.NoError(t, state.SetPostMetrics(tweet.ID, newMetrics))

	response := lookupTweetsByIDs(t, state, tweet.ID)
	data := responseData(t, response)
	require.Len(t, data, 1)
	require.Equal(t, newMetrics, metricsFromItem(t, data[0]))
}

func TestSetPostMetricsReplacesRatherThanIncrements(t *testing.T) {
	state := newMetricTestState(t)
	user := state.GetDefaultUser()
	tweet := state.CreateTweet("replacement metrics", user.ID)

	require.NoError(t, state.SetPostMetrics(tweet.ID, TweetMetrics{LikeCount: 10, RetweetCount: 2}))
	second := TweetMetrics{LikeCount: 25, RetweetCount: 7, ReplyCount: 1, QuoteCount: 3}
	require.NoError(t, state.SetPostMetrics(tweet.ID, second))

	response := lookupTweetsByIDs(t, state, tweet.ID)
	data := responseData(t, response)
	require.Equal(t, second, metricsFromItem(t, data[0]))
}

func TestSetPostMetricsEmitsNoFilteredStreamEvent(t *testing.T) {
	state := newMetricTestState(t)
	ruleTerm := fmt.Sprintf("metricstream%d", time.Now().UnixNano())
	state.CreateSearchStreamRule(ruleTerm, "metrics")

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

	require.NoError(t, state.SetPostMetrics(tweet.ID, TweetMetrics{LikeCount: 99}))
	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done

	body := recorder.Body.String()
	require.Equal(t, 1, strings.Count(body, "\"data\""), body)
	require.Contains(t, body, tweet.ID)
}

func TestLookupMultipleIDsReturnsEachPostMetrics(t *testing.T) {
	state := newMetricTestState(t)
	user := state.GetDefaultUser()
	first := state.CreateTweet("first metrics", user.ID)
	second := state.CreateTweet("second metrics", user.ID)
	firstMetrics := TweetMetrics{LikeCount: 1, RetweetCount: 2}
	secondMetrics := TweetMetrics{LikeCount: 5, ReplyCount: 6, QuoteCount: 7}
	require.NoError(t, state.SetPostMetrics(first.ID, firstMetrics))
	require.NoError(t, state.SetPostMetrics(second.ID, secondMetrics))

	response := lookupTweetsByIDs(t, state, first.ID, second.ID)
	data := responseData(t, response)
	require.Len(t, data, 2)
	require.Equal(t, firstMetrics, metricsFromItem(t, data[0]))
	require.Equal(t, secondMetrics, metricsFromItem(t, data[1]))
}

func TestLookupPartialSuccessIncludesItemErrors(t *testing.T) {
	state := newMetricTestState(t)
	user := state.GetDefaultUser()
	tweet := state.CreateTweet("partial success metrics", user.ID)
	unknownID := "999999999999999999"

	response := lookupTweetsByIDs(t, state, tweet.ID, unknownID)
	data := responseData(t, response)
	require.Len(t, data, 1)

	errors, ok := response["errors"].([]interface{})
	require.True(t, ok)
	require.Len(t, errors, 1)
	errorMap := errors[0].(map[string]interface{})
	require.Equal(t, unknownID, errorMap["resource_id"])
	require.Equal(t, "tweet", errorMap["resource_type"])
}

func TestSetPostMetricsUnknownPostDoesNotCreateState(t *testing.T) {
	state := newMetricTestState(t)
	before := len(state.GetAllTweets())

	err := state.SetPostMetrics("999999999999999998", TweetMetrics{LikeCount: 1})

	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
	require.Len(t, state.GetAllTweets(), before)
}

func TestSetPostMetricsRejectsInvalidMetricsWithoutPartialMutation(t *testing.T) {
	state := newMetricTestState(t)
	user := state.GetDefaultUser()
	tweet := state.CreateTweet("invalid metrics", user.ID)
	valid := TweetMetrics{LikeCount: 4, RetweetCount: 3, ReplyCount: 2, QuoteCount: 1}
	require.NoError(t, state.SetPostMetrics(tweet.ID, valid))

	err := state.SetPostMetrics(tweet.ID, TweetMetrics{LikeCount: 99, RetweetCount: -1})

	require.Error(t, err)
	require.Equal(t, valid, state.GetTweet(tweet.ID).PublicMetrics)
}
