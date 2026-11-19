package main

import (
	"fmt"
	"sort"

	"github.com/headwalluk/sycamore-client/core"
	"github.com/headwalluk/sycamore-client/core/protocol"
)

// The agent sidebar: every configured agent with its attention state, and
// ctrl+r's jump to the next reply waiting. The states and their order match
// the TUI's switcher (dev-notes/planning-multi-agent.md §6).

// Attention markers, as in the TUI.
const (
	markIdle         = "●"
	markWorking      = "◐"
	markUnread       = "◆"
	markError        = "!"
	markDisconnected = "○"
	markConnecting   = "…"
	markFailed       = "✗"
)

// eventAgents carries the whole agent list whenever any agent changes.
const eventAgents = "app:agents"

// agentNames lists the configured agents, grouped by team, then by name.
// Callers hold a.mu.
func (a *App) agentNames() []string {
	names := make([]string, 0, len(a.cfg.Instances))
	for name := range a.cfg.Instances {
		names = append(names, name)
	}
	sort.Slice(names, func(left, right int) bool {
		leftTeam, rightTeam := a.cfg.Instances[names[left]].Team, a.cfg.Instances[names[right]].Team
		if leftTeam != rightTeam {
			return leftTeam < rightTeam
		}
		return names[left] < names[right]
	})
	return names
}

// agentInfo describes one agent for the sidebar. Callers hold a.mu.
func (a *App) agentInfo(name string) AgentInfo {
	status, open := a.agents[name]
	state := a.pool.State(name)
	info := AgentInfo{
		Name:        name,
		DisplayName: name,
		Team:        a.cfg.Instances[name].Team,
		State:       stateName(state),
		Open:        open,
		Active:      name == a.active,
		Avatar:      a.avatars[name],
	}
	if session, _ := a.pool.Session(name); session.Agent != nil {
		if session.Agent.DisplayName != "" {
			info.DisplayName = session.Agent.DisplayName
		}
		info.Tagline = session.Agent.Tagline
		info.AccentColor = session.Agent.AccentColor
	}

	info.Mark, info.Label = markDisconnected, "disconnected"
	unread := 0
	if open {
		for id := range status.unread {
			if !(info.Active && id == status.convID) {
				unread++
			}
		}
	}
	info.Unread = unread
	switch {
	case !open:
		// Not connected; the agent itself may well be up.
	case state == core.StateFailed:
		info.Mark, info.Label = markFailed, "failed"
	case state == core.StateReconnecting:
		info.Mark, info.Label = markConnecting, "reconnecting"
	case state == core.StateConnecting:
		info.Mark, info.Label = markConnecting, "connecting"
	case state != core.StateOnline:
		// Offline: retired while idle.
	case status.turnActive:
		info.Mark, info.Label, info.Working = markWorking, "working", true
		if step := progressStatus(status.steps); step != "" {
			info.Label = "working · " + step
		}
	case status.errorUnseen:
		info.Mark, info.Label = markError, "error"
	case unread > 0:
		info.Mark, info.Label = markUnread, "unread"
	default:
		info.Mark, info.Label = markIdle, "idle"
	}
	return info
}

// agentList describes every configured agent. Callers hold a.mu.
func (a *App) agentList() []AgentInfo {
	names := a.agentNames()
	infos := make([]AgentInfo, 0, len(names))
	for _, name := range names {
		infos = append(infos, a.agentInfo(name))
	}
	return infos
}

// emitAgents sends the whole agent list.
func (a *App) emitAgents() {
	a.mu.Lock()
	if a.pool == nil {
		a.mu.Unlock()
		return
	}
	infos := a.agentList()
	a.mu.Unlock()
	a.emit(eventAgents, infos)
}

// NextUnread shows the next reply waiting: agents after the one on screen
// first (in sidebar order), then the rest of its own conversations. It
// returns false when nothing is waiting.
func (a *App) NextUnread() (bool, error) {
	a.mu.Lock()
	names := a.agentNames()
	start := 0
	for index, name := range names {
		if name == a.active {
			start = index + 1
			break
		}
	}
	targetAgent, targetConv, targetTitle := "", "", ""
	for offset := range names {
		name := names[(start+offset)%len(names)]
		status, open := a.agents[name]
		if !open || targetAgent != "" {
			continue
		}
		waiting := make([]string, 0, len(status.unread))
		for id := range status.unread {
			if !(name == a.active && id == status.convID) {
				waiting = append(waiting, id)
			}
		}
		sort.Strings(waiting)
		switch {
		case len(waiting) > 0:
			targetAgent, targetConv, targetTitle = name, waiting[0], status.titles[waiting[0]]
		case status.errorUnseen && name != a.active:
			targetAgent = name
		default:
			// Nothing waiting on this agent.
		}
	}
	shownConv := ""
	if status, open := a.agents[targetAgent]; open {
		shownConv = status.convID
	}
	a.mu.Unlock()

	if targetAgent == "" {
		return false, nil
	}
	if err := a.Connect(targetAgent); err != nil {
		return true, err
	}
	var err error
	if targetConv != "" && targetConv != shownConv {
		err = a.SwitchConversation(targetAgent, targetConv, targetTitle)
	}
	return true, err
}

// noteError flags an error for the sidebar unless it's for the
// conversation on screen.
func (a *App) noteError(agent string, status *agentStatus, convID string) {
	a.mu.Lock()
	status.errorUnseen = !(agent == a.active && convID == status.convID)
	a.mu.Unlock()
}

// rememberTitle records a conversation's title, for switching to it later
// without its list.
func rememberTitle(status *agentStatus, msg protocol.ServerMessage) {
	switch typed := msg.(type) {
	case *protocol.Welcome:
		if typed.ConversationID != nil && typed.ConversationTitle != nil {
			status.titles[*typed.ConversationID] = *typed.ConversationTitle
		}
	case *protocol.ConversationRenamed:
		status.titles[typed.ConversationID] = typed.Title
	case *protocol.ConversationsList:
		for _, summary := range typed.Conversations {
			if summary.Title != nil {
				status.titles[summary.ID] = *summary.Title
			}
		}
	default:
		// Other messages carry no titles.
	}
}

// describeAgentError is a note for an avatar problem, which is cosmetic but
// still shown rather than dropped.
func describeAgentError(what string, err error) string {
	return fmt.Sprintf("avatar: %s: %v", what, err)
}
