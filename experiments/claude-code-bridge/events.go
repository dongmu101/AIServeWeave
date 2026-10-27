package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

var (
	errCLIProtocol   = errors.New("invalid or oversized CLI event stream")
	errCLIResult     = errors.New("CLI reported an unsuccessful result")
	errMissingResult = errors.New("CLI stream ended without a successful result")
	errUnsafeTools   = errors.New("CLI advertised an unexpected tool")
)

type eventSummary struct {
	Events       int  `json:"events"`
	ToolStarts   int  `json:"tool_starts"`
	ToolTurns    int  `json:"tool_turns"`
	MessageStops int  `json:"message_stops"`
	SentinelSeen bool `json:"client_token_in_result"`
}

func inspectEvents(r io.Reader, expected string) (eventSummary, error) {
	var summary eventSummary
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), maxEventBytes+1)
	finished := false
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if len(line) > maxEventBytes || finished || summary.Events >= 10000 {
			return summary, errCLIProtocol
		}
		var event struct {
			Type    string   `json:"type"`
			Subtype string   `json:"subtype"`
			IsError bool     `json:"is_error"`
			Result  string   `json:"result"`
			Tools   []string `json:"tools"`
			Event   struct {
				Type         string `json:"type"`
				ContentBlock struct {
					Type string `json:"type"`
				} `json:"content_block"`
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
			} `json:"event"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type == "" {
			return summary, errCLIProtocol
		}
		summary.Events++
		switch event.Type {
		case "system":
			if event.Subtype == "init" {
				for _, tool := range event.Tools {
					if tool != "mcp__aisw_probe__probe_echo" && tool != "EndConversation" {
						return summary, errUnsafeTools
					}
				}
			}
		case "stream_event":
			switch event.Event.Type {
			case "content_block_start":
				if event.Event.ContentBlock.Type == "tool_use" {
					summary.ToolStarts++
				}
			case "message_delta":
				if event.Event.Delta.StopReason == "tool_use" {
					summary.ToolTurns++
				}
			case "message_stop":
				summary.MessageStops++
			}
		case "result":
			if event.IsError || event.Subtype != "success" {
				return summary, errCLIResult
			}
			finished = true
			summary.SentinelSeen = expected != "" && strings.Contains(event.Result, expected)
		}
	}
	if scanner.Err() != nil {
		return summary, errCLIProtocol
	}
	if !finished {
		return summary, errMissingResult
	}
	return summary, nil
}
