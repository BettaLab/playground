package playground

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuoteExpansionsExposeQuotedTweetAuthorAndMedia(t *testing.T) {
	config, err := LoadDefaultPlaygroundConfig()
	require.NoError(t, err)
	state := NewStateWithConfig(config)
	user := state.GetDefaultUser()
	require.NotNil(t, user)

	quoted := state.CreateTweet("quoted with media", user.ID)
	quote := state.CreateTweet("plain quote of media post", user.ID)
	require.True(t, state.AddQuotedTweetReference(quote.ID, quoted.ID))

	quotedMedia := state.CreateMedia("quoted_media_key", 3600)
	quotedMedia.Type = "photo"
	quotedMedia.URL = "http://localhost:8080/2/media/content/quoted"
	quotedMedia.Width = 419
	quotedMedia.Height = 238
	require.True(t, state.AttachMediaToTweet(quoted.ID, []string{quotedMedia.ID}))

	expansions := []string{
		"author_id",
		"attachments.media_keys",
		"referenced_tweets.id",
		"referenced_tweets.id.author_id",
		"referenced_tweets.id.attachments.media_keys",
	}
	queryParams := &QueryParams{
		Expansions:  expansions,
		TweetFields: []string{"id", "text", "author_id", "referenced_tweets", "attachments"},
		UserFields:  []string{"id", "name", "username"},
		MediaFields: []string{"media_key", "type", "url", "width", "height"},
	}

	quoteMap := FormatTweet(quote)
	addExpansionFieldsToTweet(quoteMap, quote, expansions)
	includes := buildExpansions([]*Tweet{quote}, expansions, state, nil, queryParams)

	references, ok := quoteMap["referenced_tweets"].([]ReferencedTweet)
	require.True(t, ok)
	require.Equal(t, []ReferencedTweet{{Type: "quoted", ID: quoted.ID}}, references)
	require.NotContains(t, quoteMap, "attachments")

	includedTweets := includes["tweets"].([]map[string]interface{})
	require.Len(t, includedTweets, 1)
	require.Equal(t, quoted.ID, includedTweets[0]["id"])
	require.Equal(t, []string{"quoted_media_key"}, includedTweets[0]["attachments"].(map[string]interface{})["media_keys"])

	includedUsers := includes["users"].([]map[string]interface{})
	require.NotEmpty(t, includedUsers)

	includedMedia := includes["media"].([]map[string]interface{})
	require.Len(t, includedMedia, 1)
	require.Equal(t, "quoted_media_key", includedMedia[0]["media_key"])
	require.Equal(t, "http://localhost:8080/2/media/content/quoted", includedMedia[0]["url"])
}
