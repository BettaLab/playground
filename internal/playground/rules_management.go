package playground

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type streamRuleRequest struct {
	Add    *[]streamRuleAdditionRequest `json:"add,omitempty"`
	Delete *streamRuleDeleteRequest     `json:"delete,omitempty"`
}

type streamRuleAdditionRequest struct {
	Value string  `json:"value"`
	Tag   *string `json:"tag,omitempty"`
}

type streamRuleDeleteRequest struct {
	IDs    *[]string `json:"ids,omitempty"`
	Values *[]string `json:"values,omitempty"`
}

var numericRuleID = regexp.MustCompile(`^[0-9]{1,19}$`)
var rulePaginationToken = regexp.MustCompile(`^[0-9A-V]{16}$`)

const (
	defaultRulePageSize = 1000
	maxRulePageSize     = 1000
)

func handleSearchStreamRuleList(r *http.Request, state *State) ([]byte, int) {
	if state == nil {
		return marshalRuleResponse(map[string]interface{}{
			"title": "Server Error", "detail": "Internal server error",
			"type": "https://api.x.com/2/problems/server-error",
		}, http.StatusInternalServerError)
	}

	ids, normalizedIDs, err := parseRuleIDsFilter(r)
	if err != nil {
		return ruleRequestError("ids", err.Error())
	}
	maxResults, err := parseRuleMaxResults(r)
	if err != nil {
		return ruleRequestError("max_results", err.Error())
	}

	allRules := state.GetSearchStreamRules()
	eligible := allRules
	missingIDs := []string(nil)
	if ids != nil {
		requested := make(map[string]bool, len(ids))
		for _, id := range ids {
			requested[id] = true
		}
		eligible = make([]*SearchStreamRule, 0, len(ids))
		for _, rule := range allRules {
			if requested[rule.ID] {
				eligible = append(eligible, rule)
				delete(requested, rule.ID)
			}
		}
		for _, id := range ids {
			if requested[id] {
				missingIDs = append(missingIDs, id)
			}
		}
	}

	signature := ruleListSignature(eligible, normalizedIDs, maxResults)
	offset := 0
	if tokens, present := r.URL.Query()["pagination_token"]; present {
		if len(tokens) != 1 || tokens[0] == "" {
			return ruleRequestError("pagination_token", "must be one nonblank 16-character base32hex cursor")
		}
		offset, err = decodeRulePaginationToken(tokens[0], signature, len(eligible))
		if err != nil {
			return ruleRequestError("pagination_token", err.Error())
		}
	}
	end := offset + maxResults
	if end > len(eligible) {
		end = len(eligible)
	}
	page := eligible[offset:end]

	dataItems := make([]map[string]interface{}, 0, len(page))
	for _, rule := range page {
		item := map[string]interface{}{"id": rule.ID, "value": rule.Value}
		if rule.Tag != "" {
			item["tag"] = rule.Tag
		}
		dataItems = append(dataItems, item)
	}
	meta := map[string]interface{}{"result_count": len(dataItems)}
	if end < len(eligible) {
		meta["next_token"] = encodeRulePaginationToken(end, signature)
	}
	response := map[string]interface{}{"data": dataItems, "meta": meta}
	if len(missingIDs) > 0 {
		errors := make([]map[string]interface{}, 0, len(missingIDs))
		for _, id := range missingIDs {
			errors = append(errors, map[string]interface{}{
				"value":         id,
				"title":         "Not Found Error",
				"detail":        "Could not find rule with the requested ID.",
				"type":          "https://api.x.com/2/problems/resource-not-found",
				"parameter":     "ids",
				"resource_id":   id,
				"resource_type": "rule",
				"status":        http.StatusNotFound,
			})
		}
		response["errors"] = errors
	}
	return marshalRuleResponse(response, http.StatusOK)
}

func parseRuleIDsFilter(r *http.Request) ([]string, string, error) {
	values, present := r.URL.Query()["ids"]
	if !present {
		return nil, "", nil
	}
	if len(values) != 1 || values[0] == "" {
		return nil, "", fmt.Errorf("must be one comma-separated list of numeric rule IDs")
	}
	parts := strings.Split(values[0], ",")
	if len(parts) > 1000 {
		return nil, "", fmt.Errorf("must contain at most 1000 IDs")
	}
	seen := make(map[string]bool, len(parts))
	ids := make([]string, 0, len(parts))
	for _, id := range parts {
		if !numericRuleID.MatchString(id) {
			return nil, "", fmt.Errorf("must contain only numeric rule IDs")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	normalized := append([]string(nil), ids...)
	sort.Strings(normalized)
	return ids, strings.Join(normalized, ","), nil
}

func parseRuleMaxResults(r *http.Request) (int, error) {
	values, present := r.URL.Query()["max_results"]
	if !present {
		return defaultRulePageSize, nil
	}
	if len(values) != 1 {
		return 0, fmt.Errorf("must be provided once")
	}
	value, err := strconv.Atoi(values[0])
	if err != nil || value < 1 || value > maxRulePageSize {
		return 0, fmt.Errorf("must be an integer between 1 and 1000")
	}
	return value, nil
}

func ruleListSignature(rules []*SearchStreamRule, normalizedIDs string, maxResults int) [5]byte {
	hash := sha256.New()
	for _, rule := range rules {
		hash.Write([]byte(rule.ID))
		hash.Write([]byte{0})
	}
	hash.Write([]byte(normalizedIDs))
	hash.Write([]byte{0})
	hash.Write([]byte(strconv.Itoa(maxResults)))
	sum := hash.Sum(nil)
	var signature [5]byte
	copy(signature[:], sum[:5])
	return signature
}

func encodeRulePaginationToken(offset int, signature [5]byte) string {
	payload := make([]byte, 10)
	payload[0] = 1
	binary.BigEndian.PutUint32(payload[1:5], uint32(offset))
	copy(payload[5:], signature[:])
	return base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(payload)
}

func decodeRulePaginationToken(token string, signature [5]byte, eligibleCount int) (int, error) {
	if !rulePaginationToken.MatchString(token) {
		return 0, fmt.Errorf("must be a 16-character base32hex cursor")
	}
	payload, err := base32.HexEncoding.WithPadding(base32.NoPadding).DecodeString(token)
	if err != nil || len(payload) != 10 || payload[0] != 1 {
		return 0, fmt.Errorf("is invalid or expired")
	}
	if !bytes.Equal(payload[5:], signature[:]) {
		return 0, fmt.Errorf("is invalid or expired")
	}
	offset := int(binary.BigEndian.Uint32(payload[1:5]))
	if offset <= 0 || offset >= eligibleCount {
		return 0, fmt.Errorf("is invalid or expired")
	}
	return offset, nil
}

func handleSearchStreamRuleMutation(r *http.Request, state *State) ([]byte, int) {
	if state == nil {
		return marshalRuleResponse(map[string]interface{}{
			"title": "Server Error", "detail": "Internal server error",
			"type": "https://api.x.com/2/problems/server-error",
		}, http.StatusInternalServerError)
	}

	dryRun, err := parseRuleBoolQuery(r, "dry_run")
	if err != nil {
		return ruleRequestError("dry_run", err.Error())
	}
	deleteAll, err := parseRuleBoolQuery(r, "delete_all")
	if err != nil {
		return ruleRequestError("delete_all", err.Error())
	}

	var request streamRuleRequest
	if r.Body != nil {
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&request)
		if err == io.EOF && deleteAll {
			err = nil
		}
		if err == nil {
			var extra interface{}
			if extraErr := decoder.Decode(&extra); extraErr != io.EOF {
				err = fmt.Errorf("request must contain one JSON object")
			}
		}
	}
	if err != nil {
		return ruleRequestError("requestBody", fmt.Sprintf("invalid JSON: %v", err))
	}

	operations := 0
	if request.Add != nil {
		operations++
	}
	if request.Delete != nil {
		operations++
	}
	if deleteAll {
		operations++
	}
	if operations != 1 {
		return ruleRequestError("requestBody", "exactly one of add, delete, or delete_all=true is required")
	}
	if deleteAll {
		return buildDeleteRuleResponse(state.DeleteAllSearchStreamRules(dryRun), dryRun)
	}
	if request.Add != nil {
		return handleRuleAdd(state, *request.Add, dryRun)
	}
	return handleRuleDelete(state, *request.Delete, dryRun)
}

func parseRuleBoolQuery(r *http.Request, name string) (bool, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("must be true or false")
	}
	return parsed, nil
}

func handleRuleAdd(state *State, items []streamRuleAdditionRequest, dryRun bool) ([]byte, int) {
	if len(items) == 0 {
		return ruleRequestError("add", "must be a non-empty list")
	}
	_, _, maxLength := state.SearchStreamRuleLimits()
	valid := make([]SearchStreamRuleAddition, 0, len(items))
	errors := make([]SearchStreamRuleItemError, 0)
	for _, item := range items {
		tag := ""
		if item.Tag != nil {
			tag = *item.Tag
		}
		if message := validateRuleValue(item.Value, maxLength); message != "" {
			errors = append(errors, SearchStreamRuleItemError{Value: item.Value, Message: message, Kind: "invalid"})
			continue
		}
		if item.Tag != nil && !validRuleTag(tag) {
			errors = append(errors, SearchStreamRuleItemError{Value: item.Value, Message: "Tag must be nonblank, contain no control characters, and be at most 255 characters", Kind: "invalid"})
			continue
		}
		valid = append(valid, SearchStreamRuleAddition{Value: item.Value, Tag: tag})
	}
	result := state.AddSearchStreamRules(valid, dryRun)
	result.Errors = append(errors, result.Errors...)
	return buildAddRuleResponse(result, dryRun)
}

func handleRuleDelete(state *State, request streamRuleDeleteRequest, dryRun bool) ([]byte, int) {
	selectors := 0
	if request.IDs != nil {
		selectors++
	}
	if request.Values != nil {
		selectors++
	}
	if selectors != 1 {
		return ruleRequestError("delete", "exactly one of ids or values is required")
	}

	ids := []string(nil)
	values := []string(nil)
	errors := make([]SearchStreamRuleItemError, 0)
	if request.IDs != nil {
		if len(*request.IDs) == 0 {
			return ruleRequestError("delete.ids", "must be a non-empty list")
		}
		for _, id := range *request.IDs {
			if !numericRuleID.MatchString(id) {
				errors = append(errors, SearchStreamRuleItemError{ID: id, Message: "Rule ID must be a numeric string", Kind: "invalid"})
				continue
			}
			ids = append(ids, id)
		}
	} else {
		if len(*request.Values) == 0 {
			return ruleRequestError("delete.values", "must be a non-empty list")
		}
		for _, value := range *request.Values {
			if strings.TrimSpace(value) == "" {
				errors = append(errors, SearchStreamRuleItemError{Value: value, Message: "Rule value must be nonblank", Kind: "invalid"})
				continue
			}
			values = append(values, value)
		}
	}
	result := state.DeleteSearchStreamRules(ids, values, dryRun)
	result.Errors = append(errors, result.Errors...)
	return buildDeleteRuleResponse(result, dryRun)
}

// validateRuleValue intentionally validates only structural defects and the
// operator forms exercised by Atalaya. It is not a replacement for X validation.
func validateRuleValue(value string, maxLength int) string {
	if strings.TrimSpace(value) == "" {
		return "Rule value must be nonblank"
	}
	if utf8.RuneCountInString(value) > maxLength {
		return fmt.Sprintf("Rule value exceeds the %d character limit", maxLength)
	}
	depth := 0
	quoted := false
	escaped := false
	for _, character := range value {
		if escaped {
			escaped = false
			continue
		}
		if quoted && character == '\\' {
			escaped = true
			continue
		}
		if character == '"' {
			quoted = !quoted
			continue
		}
		if quoted {
			continue
		}
		switch character {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return "Rule value has unbalanced parentheses"
			}
		}
	}
	if quoted {
		return "Rule value has an unterminated quoted phrase"
	}
	if depth != 0 {
		return "Rule value has unbalanced parentheses"
	}
	trimmed := strings.TrimSpace(value)
	upper := strings.ToUpper(trimmed)
	if regexp.MustCompile(`\(\s*\)`).MatchString(trimmed) {
		return "Rule value contains an empty Boolean group"
	}
	if upper == "OR" || upper == "AND" || strings.HasPrefix(upper, "OR ") || strings.HasPrefix(upper, "AND ") || strings.HasSuffix(upper, " OR") || strings.HasSuffix(upper, " AND") || strings.Contains(upper, " OR OR ") || strings.Contains(upper, " AND AND ") || regexp.MustCompile(`(?i)(\(\s*(OR|AND)\b|\b(OR|AND)\s*\))`).MatchString(trimmed) {
		return "Rule value contains an incomplete Boolean expression"
	}
	for _, token := range strings.Fields(value) {
		clean := strings.Trim(token, "()")
		clean = strings.TrimPrefix(clean, "-")
		lower := strings.ToLower(clean)
		for _, prefix := range []string{"from:", "lang:"} {
			if lower == prefix {
				return fmt.Sprintf("Operator %s requires a value", prefix)
			}
		}
		if lower == "has:" || lower == "is:" {
			return fmt.Sprintf("Operator %s requires a value", lower)
		}
	}
	return ""
}

func validRuleTag(tag string) bool {
	if strings.TrimSpace(tag) == "" || utf8.RuneCountInString(tag) > 255 {
		return false
	}
	for _, character := range tag {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func buildAddRuleResponse(result SearchStreamRuleMutationResult, dryRun bool) ([]byte, int) {
	dataItems := make([]map[string]interface{}, 0, len(result.Created))
	for _, rule := range result.Created {
		item := map[string]interface{}{"value": rule.Value}
		if rule.ID != "" {
			item["id"] = rule.ID
		}
		if rule.Tag != "" {
			item["tag"] = rule.Tag
		}
		dataItems = append(dataItems, item)
	}
	response := mutationEnvelope(dryRun, "created", len(result.Created), "not_created", len(result.Errors))
	summary := response["meta"].(map[string]interface{})["summary"].(map[string]interface{})
	summary["valid"] = len(result.Created)
	summary["invalid"] = len(result.Errors)
	if len(dataItems) > 0 {
		response["data"] = dataItems
	}
	appendRuleErrors(response, result.Errors)
	return marshalRuleResponse(response, http.StatusOK)
}

func buildDeleteRuleResponse(result SearchStreamRuleMutationResult, dryRun bool) ([]byte, int) {
	response := mutationEnvelope(dryRun, "deleted", len(result.Deleted), "not_deleted", len(result.Errors))
	appendRuleErrors(response, result.Errors)
	return marshalRuleResponse(response, http.StatusOK)
}

func mutationEnvelope(_ bool, successName string, success int, failureName string, failure int) map[string]interface{} {
	summary := map[string]interface{}{successName: success, failureName: failure}
	response := map[string]interface{}{
		"meta": map[string]interface{}{
			"sent":    time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
			"summary": summary,
		},
	}
	return response
}

func appendRuleErrors(response map[string]interface{}, itemErrors []SearchStreamRuleItemError) {
	if len(itemErrors) == 0 {
		return
	}
	errors := make([]map[string]interface{}, 0, len(itemErrors))
	for _, itemError := range itemErrors {
		item := map[string]interface{}{
			"title":  itemError.Kind,
			"detail": itemError.Message,
			"type":   "https://api.x.com/2/problems/invalid-rules",
		}
		if itemError.ID != "" {
			item["id"] = itemError.ID
		}
		if itemError.Value != "" {
			item["value"] = itemError.Value
		}
		errors = append(errors, item)
	}
	response["errors"] = errors
}

func ruleRequestError(parameter, message string) ([]byte, int) {
	return marshalRuleResponse(map[string]interface{}{
		"title":  "Invalid Request",
		"detail": message,
		"type":   "https://api.x.com/2/problems/invalid-request",
		"errors": []map[string]interface{}{{"parameter": parameter, "message": message}},
	}, http.StatusBadRequest)
}

func marshalRuleResponse(response map[string]interface{}, status int) ([]byte, int) {
	data, marshalStatus := MarshalJSONResponse(response)
	if marshalStatus != http.StatusOK {
		return data, marshalStatus
	}
	return data, status
}
