package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/OmniBBFB/agent-done/internal/remote"
)

func controlConnections(session Session) []string {
	var connections []string
	for _, view := range session.Views {
		if !liveView(view) || !validProxyID(view.Connection) || !exists(proxyControlSocket(view.Connection)) {
			continue
		}
		connections = append(connections, view.Connection)
	}
	return connections
}

func messageConnection(session Session) string {
	connections := controlConnections(session)
	if len(connections) != 1 {
		return ""
	}
	views := 0
	for _, view := range session.Views {
		if liveView(view) {
			views++
		}
	}
	if views != 1 {
		return ""
	}
	return connections[0]
}

func (c codexCatalog) Interactions(ctx context.Context, sessionID, cursor string, limit int) (remote.Page[remote.Interaction], error) {
	page := remote.Page[remote.Interaction]{Items: []remote.Interaction{}}
	snapshot, err := c.snapshot()
	if err != nil {
		return page, err
	}
	var cards []remote.Interaction
	for _, session := range snapshot.Sessions {
		if sessionID != "" && session.ID != sessionID {
			continue
		}
		for _, connection := range controlConnections(session) {
			result, err := callProxy(ctx, connection, proxyRequest{Operation: "list", Session: session.ID})
			if ctx.Err() != nil {
				return page, ctx.Err()
			}
			if err == nil {
				cards = append(cards, result.Cards...)
			}
		}
	}
	// The cursor is the last UUID returned. A deleted card doesn't invalidate
	// the rest of the snapshot.
	sort.Slice(cards, func(i, j int) bool { return cards[i].ID < cards[j].ID })
	bytes := 0
	for _, card := range cards {
		if card.ID <= cursor {
			continue
		}
		encoded, err := json.Marshal(card)
		if err != nil {
			return page, err
		}
		if len(page.Items) == limit || bytes+len(encoded) > 240<<10 {
			next := page.Items[len(page.Items)-1].ID
			page.NextCursor = &next
			break
		}
		page.Items = append(page.Items, card)
		bytes += len(encoded)
	}
	return page, nil
}

func (c codexCatalog) Respond(ctx context.Context, session, interaction string, reply remote.InteractionReply) (string, error) {
	owner, status, err := c.interactionOwner(ctx, session, interaction)
	if err != nil || status != "pending" {
		return status, err
	}
	result, err := callProxy(ctx, owner, proxyRequest{
		Operation: "respond", Session: session, Interaction: interaction, Reply: reply,
	})
	if result.Status == "missing" {
		return "closed", nil
	}
	return result.Status, err
}

func (c codexCatalog) InteractionStatus(ctx context.Context, session, interaction string) (string, error) {
	_, status, err := c.interactionOwner(ctx, session, interaction)
	return status, err
}

func (c codexCatalog) interactionOwner(ctx context.Context, session, interaction string) (string, string, error) {
	snapshot, err := c.snapshot()
	if err != nil {
		return "", "", err
	}
	for _, entry := range snapshot.Sessions {
		if entry.ID != session {
			continue
		}
		connections := controlConnections(entry)
		if len(connections) == 0 {
			break
		}
		sawLiveProxy := false
		for _, connection := range connections {
			result, err := callProxy(ctx, connection, proxyRequest{
				Operation: "status", Session: session, Interaction: interaction,
			})
			var problem *remote.Problem
			if errors.As(err, &problem) && problem.Code == "forbidden" {
				return "", "", problem
			}
			if err != nil {
				continue
			}
			sawLiveProxy = true
			if result.Status != "missing" {
				return connection, result.Status, nil
			}
		}
		if sawLiveProxy {
			return "", "closed", nil
		}
	}
	if ctx.Err() != nil {
		return "", "", ctx.Err()
	}
	return "", "expired", nil
}

func (c codexCatalog) Start(ctx context.Context, session, intent, text string, expires int64) (string, error) {
	snapshot, err := c.snapshot()
	if err != nil {
		return "", err
	}
	for _, entry := range snapshot.Sessions {
		if entry.ID != session {
			continue
		}
		connection := messageConnection(entry)
		if connection == "" {
			return "", &remote.Problem{Code: "unavailable"}
		}
		switch entry.Phase {
		case "running", "approval", "question":
			return "", &remote.Problem{Code: "busy"}
		}
		if expires <= time.Now().UnixMilli() {
			return "expired", nil
		}
		result, err := callProxy(ctx, connection, proxyRequest{
			Operation: "start", Session: session, Intent: intent, Text: text, Expires: expires,
		})
		return result.Status, err
	}
	return "", &remote.Problem{Code: "forbidden"}
}
