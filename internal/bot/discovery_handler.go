package bot

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/pyed/CordBrief/internal/state"
)

const discoveryPageSize = 8

func (b *Bot) showDiscovery(ctx context.Context, chatID int64, messageID int, guild string, page int, load, refresh bool) {
	b.mu.Lock()
	items, cached := b.discoveryCache[guild]
	b.mu.Unlock()
	if refresh || (!cached && load) {
		err := b.mutateState(func() error {
			if b.dceManager == nil {
				return errors.New("DCE is not configured.")
			}
			discovered, err := b.dceManager.Discover(ctx, guild)
			if err != nil {
				return err
			}
			items, cached = discovered, true
			b.mu.Lock()
			b.discoveryCache[guild] = discovered
			b.mu.Unlock()
			return nil
		})
		if err != nil {
			b.sendTextMessage(ctx, chatID, "Discovery: "+err.Error())
			return
		}
	}
	if !cached {
		b.sendTextMessage(ctx, chatID, "Discovery cache expired. Open /discover again.")
		return
	}
	cfg, err := b.store.LoadConfig()
	if err != nil {
		b.sendTextMessage(ctx, chatID, "Cannot load configuration.")
		return
	}
	visible := items
	if guild != "" {
		visible = nil
		// ponytail: linear membership scans; use ID sets if large hidden lists make paging slow.
		for _, ch := range items {
			if !containsChannel(cfg.Channels, ch.ID) && !containsChannel(cfg.HiddenChannels, ch.ID) {
				visible = append(visible, ch)
			}
		}
	}
	page, start, end := discoveryPage(page, len(visible))
	var rows [][]models.InlineKeyboardButton
	for _, item := range visible[start:end] {
		action := "d:open:" + item.ID
		if guild != "" {
			action = "d:pick:" + guild + ":" + item.ID
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: discoveryLabel(item.Name), CallbackData: action}})
	}
	rows = append(rows, discoveryNavigation("d:page:"+guild+":", page, len(visible)))
	rows = append(rows, []models.InlineKeyboardButton{{Text: "Refresh", CallbackData: "d:refresh:" + guild}, {Text: "Hidden channels", CallbackData: "d:hidden:0"}})
	text := "Discovered servers"
	if guild != "" {
		text = "Discovered channels · server " + guild + "\nFollowed and hidden channels are omitted."
		rows = append(rows, []models.InlineKeyboardButton{{Text: "Back to servers", CallbackData: "d:page::0"}})
	}
	text += fmt.Sprintf("\n%d shown · page %d/%d\n\nDCE's catalog may differ from Discord's UI. Visibility and read access are not inferred. Select a channel to follow or hide it.", len(visible), page+1, max(1, (len(visible)+discoveryPageSize-1)/discoveryPageSize))
	b.renderOrEdit(ctx, chatID, messageID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func containsChannel(items []state.ChannelConfig, id string) bool {
	return slices.ContainsFunc(items, func(ch state.ChannelConfig) bool { return ch.ID == id })
}

func discoveryLabel(name string) string {
	runes := []rune(name)
	if len(runes) > 70 {
		return string(runes[:69]) + "…"
	}
	return name
}

func discoveryPage(page, count int) (int, int, int) {
	page = max(0, min(page, max(0, (count-1)/discoveryPageSize)))
	start := page * discoveryPageSize
	return page, start, min(start+discoveryPageSize, count)
}

func discoveryNavigation(prefix string, page, count int) []models.InlineKeyboardButton {
	row := []models.InlineKeyboardButton{}
	if page > 0 {
		row = append(row, models.InlineKeyboardButton{Text: "Previous", CallbackData: prefix + strconv.Itoa(page-1)})
	}
	if (page+1)*discoveryPageSize < count {
		row = append(row, models.InlineKeyboardButton{Text: "Next", CallbackData: prefix + strconv.Itoa(page+1)})
	}
	if len(row) == 0 {
		row = append(row, models.InlineKeyboardButton{Text: "End of list", CallbackData: "noop"})
	}
	return row
}

func (b *Bot) showHidden(ctx context.Context, chatID int64, messageID, page int) {
	cfg, err := b.store.LoadConfig()
	if err != nil {
		b.sendTextMessage(ctx, chatID, "Cannot load configuration.")
		return
	}
	page, start, end := discoveryPage(page, len(cfg.HiddenChannels))
	var rows [][]models.InlineKeyboardButton
	for _, ch := range cfg.HiddenChannels[start:end] {
		rows = append(rows, []models.InlineKeyboardButton{{Text: "Unhide " + discoveryLabel(ch.Name) + " (" + ch.ID + ")", CallbackData: "d:unhide:" + ch.ID}})
	}
	rows = append(rows, discoveryNavigation("d:hidden:", page, len(cfg.HiddenChannels)))
	rows = append(rows, []models.InlineKeyboardButton{{Text: "Back to servers", CallbackData: "d:page::0"}})
	b.renderOrEdit(ctx, chatID, messageID, "Hidden channels\nSelect to unhide. Hiding only affects this browser; it does not unfollow a channel.", &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) handleDiscoveryCallback(ctx context.Context, chatID int64, messageID int, data string) {
	p := strings.Split(data, ":")
	if len(p) < 3 {
		return
	}
	switch p[1] {
	case "page":
		if len(p) != 4 {
			return
		}
		page, err := strconv.Atoi(p[3])
		if err != nil {
			return
		}
		b.showDiscovery(ctx, chatID, messageID, p[2], page, false, false)
	case "open", "refresh":
		if len(p) != 3 {
			return
		}
		if p[2] != "" {
			b.mu.Lock()
			known := containsChannel(b.discoveryCache[""], p[2])
			b.mu.Unlock()
			if !known {
				b.sendTextMessage(ctx, chatID, "Server list changed. Open /discover again.")
				return
			}
		}
		b.showDiscovery(ctx, chatID, messageID, p[2], 0, true, p[1] == "refresh")
	case "hidden":
		if len(p) != 3 {
			return
		}
		page, err := strconv.Atoi(p[2])
		if err == nil {
			b.showHidden(ctx, chatID, messageID, page)
		}
	case "unhide":
		if len(p) != 3 {
			return
		}
		if err := b.setHidden(state.ChannelConfig{ID: p[2]}, false); err != nil {
			b.sendTextMessage(ctx, chatID, err.Error())
			return
		}
		b.showHidden(ctx, chatID, messageID, 0)
	case "pick", "follow", "hide":
		if len(p) != 4 {
			return
		}
		b.mu.Lock()
		items := b.discoveryCache[p[2]]
		idx := slices.IndexFunc(items, func(ch state.ChannelConfig) bool { return ch.ID == p[3] })
		b.mu.Unlock()
		if idx < 0 {
			b.sendTextMessage(ctx, chatID, "Channel list changed. Open /discover again.")
			return
		}
		ch := items[idx]
		switch p[1] {
		case "follow":
			b.handleFollow(ctx, chatID, []string{ch.ID, ch.Name})
		case "hide":
			if err := b.setHidden(ch, true); err != nil {
				b.sendTextMessage(ctx, chatID, err.Error())
				return
			}
			b.showDiscovery(ctx, chatID, messageID, p[2], 0, false, false)
		case "pick":
			b.renderOrEdit(ctx, chatID, messageID, "Channel: "+ch.Name+"\nID: "+ch.ID, &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
				{{Text: "Follow", CallbackData: "d:follow:" + p[2] + ":" + ch.ID}, {Text: "Hide", CallbackData: "d:hide:" + p[2] + ":" + ch.ID}},
				{{Text: "Back to channels", CallbackData: "d:page:" + p[2] + ":0"}},
			}})
		}
	}
}

func (b *Bot) setHidden(ch state.ChannelConfig, hidden bool) error {
	return b.mutateState(func() error {
		cfg, err := b.store.LoadConfig()
		if err != nil {
			return err
		}
		cfg.HiddenChannels = slices.DeleteFunc(cfg.HiddenChannels, func(item state.ChannelConfig) bool { return item.ID == ch.ID })
		if hidden {
			cfg.HiddenChannels = append(cfg.HiddenChannels, ch)
		}
		return b.store.SaveConfig(cfg)
	})
}
