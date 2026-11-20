package main

import (
	"slices"
	"strings"

	"github.com/OmniBBFB/agent-done/internal/remote"
)

func codexInteraction(method string, params map[string]any, session string) (remote.Interaction, bool) {
	card := remote.Interaction{Session: session, ID: remote.NewID()}
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		card.Kind = "approval"
		card.Summary, _ = clipText(str(params["reason"]), 1024)
		allow := false
		card.CanApprove = &allow
		if method == "item/fileChange/requestApproval" {
			if card.Summary == "" {
				card.Summary = "Codex 请求修改文件"
			}
			return card, true
		}
		if card.Summary == "" {
			card.Summary = "Codex 请求执行命令"
		}
		command, cwd := str(params["command"]), str(params["cwd"])
		if params["networkApprovalContext"] == nil && strings.TrimSpace(command) != "" &&
			strings.TrimSpace(cwd) != "" && len(command) <= 4096 && len(cwd) <= 1024 {
			allow = true
			card.Details = &remote.ApprovalDetails{Kind: "command", Command: command, CWD: cwd}
		}
		return card, true
	case "item/tool/requestUserInput":
		if params["isBlocking"] == false {
			return remote.Interaction{}, false
		}
		card.Kind = "question"

		questions, _ := params["questions"].([]any)
		if len(questions) == 0 || len(questions) > 16 {
			return remote.Interaction{}, false
		}
		seen := make(map[string]bool)
		for _, value := range questions {
			question := object(value)
			id, prompt := str(question["id"]), str(question["question"])
			if question["isSecret"] == true || id == "" || len(id) > 256 || seen[id] ||
				strings.TrimSpace(prompt) == "" || len(prompt) > 1024 {
				return remote.Interaction{}, false
			}
			seen[id] = true
			options, _ := question["options"].([]any)
			if len(options) > 12 {
				return remote.Interaction{}, false
			}
			labels := []string{}
			for _, option := range options {
				label := str(object(option)["label"])
				if strings.TrimSpace(label) == "" || len(label) > 256 || slices.Contains(labels, label) {
					return remote.Interaction{}, false
				}
				labels = append(labels, label)
			}
			card.Questions = append(card.Questions, remote.Question{
				ID: id, Text: prompt, Options: labels, AllowsText: question["isOther"] == true || len(labels) == 0,
			})
		}
		return card, true
	}
	return remote.Interaction{}, false
}

func codexInteractionResult(card remote.Interaction, reply remote.InteractionReply) (any, *remote.Problem) {
	invalid := &remote.Problem{Code: "invalid_request"}
	switch card.Kind {
	case "approval":
		if reply.Kind != "approval" {
			return nil, invalid
		}
		switch reply.Decision {
		case "deny":
			return map[string]string{"decision": "decline"}, nil
		case "allow_once":
			if card.CanApprove == nil || !*card.CanApprove {
				return nil, &remote.Problem{Code: "forbidden"}
			}
			return map[string]string{"decision": "accept"}, nil
		default:
			return nil, invalid
		}
	case "question":
		if reply.Kind != "answer" || len(reply.Answers) != len(card.Questions) {
			return nil, invalid
		}
		answers := make(map[string]map[string][]string, len(card.Questions))
		for _, question := range card.Questions {
			values := reply.Answers[question.ID]
			if len(values) != 1 || strings.TrimSpace(values[0]) == "" || len(values[0]) > 16<<10 ||
				!question.AllowsText && !slices.Contains(question.Options, values[0]) {
				return nil, invalid
			}
			answers[question.ID] = map[string][]string{"answers": values}
		}
		return map[string]any{"answers": answers}, nil
	}
	return nil, invalid
}
