// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"context"
	"errors"
	"time"

	"github.com/icavar/yacm/internal/sshconn"
	"github.com/icavar/yacm/internal/store"
)

// ---- prompts -----------------------------------------------------------------

// PromptAnswer is the UI's reply to a prompt event.
type PromptAnswer struct {
	Accept  bool     `json:"accept"`
	Value   string   `json:"value"`
	Save    bool     `json:"save"`
	Answers []string `json:"answers"`
}

// AnswerPrompt delivers the user's answer.
func (a *App) AnswerPrompt(id string, ans PromptAnswer) {
	a.promptMu.Lock()
	ch, ok := a.prompts[id]
	delete(a.prompts, id)
	a.promptMu.Unlock()
	if ok {
		ch <- ans
	}
}

func (a *App) ask(ctx context.Context, payload map[string]any) (PromptAnswer, error) {
	id := store.NewID()
	ch := make(chan PromptAnswer, 1)
	a.promptMu.Lock()
	a.prompts[id] = ch
	a.promptMu.Unlock()
	payload["id"] = id
	a.emit("prompt", payload)
	defer func() {
		a.promptMu.Lock()
		delete(a.prompts, id)
		a.promptMu.Unlock()
		a.emit("prompt:closed", id)
	}()
	select {
	case ans := <-ch:
		return ans, nil
	case <-ctx.Done():
		return PromptAnswer{}, ctx.Err()
	case <-time.After(10 * time.Minute):
		return PromptAnswer{}, errors.New("prompt timed out")
	}
}

type tabPrompter struct {
	a     *App
	tabID string
	name  string
}

func (p tabPrompter) HostKey(ctx context.Context, info sshconn.HostKeyInfo) (bool, error) {
	ans, err := p.a.ask(ctx, map[string]any{"kind": "hostkey", "tabId": p.tabID, "session": p.name, "hostKey": info})
	return ans.Accept, err
}

func (p tabPrompter) Secret(ctx context.Context, req sshconn.SecretRequest) (string, bool, error) {
	if req.CanSave && req.Kind != "username" && !p.a.vault.Unlocked() {
		req.CanSave = false
	}
	ans, err := p.a.ask(ctx, map[string]any{"kind": "secret", "tabId": p.tabID, "session": p.name, "secret": req})
	if err != nil {
		return "", false, err
	}
	if !ans.Accept {
		return "", false, errors.New("cancelled")
	}
	return ans.Value, ans.Save, nil
}

func (p tabPrompter) Questions(ctx context.Context, title, instruction string, qs []string, echos []bool) ([]string, error) {
	ans, err := p.a.ask(ctx, map[string]any{"kind": "questions", "tabId": p.tabID, "session": p.name, "title": title, "instruction": instruction, "questions": qs, "echos": echos})
	if err != nil {
		return nil, err
	}
	if !ans.Accept || len(ans.Answers) != len(qs) {
		return nil, errors.New("cancelled")
	}
	return ans.Answers, nil
}
