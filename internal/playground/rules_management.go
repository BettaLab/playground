package playground

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
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

var numericRuleID = regexp.MustCompile(`^[0-9]+$`)

func handleSearchStreamRuleMutation(r *http.Request, state *State) ([]byte, int) {
	if state == nil {
		return marshalRuleResponse(map[string]interface{}{
			"title": "Server Error", "detail": "Internal server error",
			"type": "https://api.twitter.com/2/problems/server-error",
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

func mutationEnvelope(dryRun bool, successName string, success int, failureName string, failure int) map[string]interface{} {
	summary := map[string]interface{}{successName: success, failureName: failure}
	response := map[string]interface{}{
		"meta": map[string]interface{}{
			"sent":    time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
			"summary": summary,
		},
	}
	if dryRun {
		response["meta"].(map[string]interface{})["dry_run"] = true
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
			"type":   "https://api.twitter.com/2/problems/invalid-rules",
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
		"type":   "https://api.twitter.com/2/problems/invalid-request",
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
