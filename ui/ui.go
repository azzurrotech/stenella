package ui

import (
	"fmt"
	"image/color"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"stenella/card"
	"stenella/storage"
)

var (
	cfBlue    = color.RGBA{0x64, 0x95, 0xed, 0xff}
	colMeta   = color.RGBA{0x88, 0x88, 0xa0, 0xff}
	colDiv    = color.RGBA{0xd8, 0xd8, 0xe6, 0xff}
	colGreyBg = color.RGBA{0xee, 0xee, 0xee, 0xff}
	colPreBg  = color.RGBA{0xf8, 0xf8, 0xfc, 0xff}

	cardColors = []color.Color{
		color.RGBA{0x64, 0x95, 0xed, 0xff},
		color.RGBA{0xd3, 0x3a, 0x3a, 0xff},
		color.RGBA{0x2e, 0x7d, 0x32, 0xff},
		color.RGBA{0xd4, 0xa0, 0x17, 0xff},
		color.White,
	}
)

type App struct {
	win       fyne.Window
	store     *storage.Store
	tabs      *container.AppTabs
	cardList  *fyne.Container
	detailBox *fyne.Container
	selected  *card.Card
	input     *widget.Entry
}

func New(s *storage.Store) *App {
	return &App{store: s}
}

func (a *App) Run() {
	a.win = app.New().NewWindow("Stenella")
	a.win.Resize(fyne.NewSize(1000, 700))
	a.buildUI()
	a.win.ShowAndRun()
}

func (a *App) buildUI() {
	topBg := canvas.NewRectangle(cfBlue)
	title := canvas.NewText("Stenella", color.White)
	title.TextSize = 20
	title.TextStyle = fyne.TextStyle{Bold: true}
	topBar := container.NewStack(topBg, container.NewCenter(title))

	a.tabs = container.NewAppTabs()
	a.tabs.Append(a.buildListTab())
	a.tabs.Append(a.buildDetailTab())
	a.tabs.SetTabLocation(container.TabLocationBottom)

	content := container.NewBorder(topBar, nil, nil, nil, a.tabs)
	a.win.SetContent(content)
	a.refreshList()
}

func (a *App) buildListTab() *container.TabItem {
	input := widget.NewEntry()
	input.SetPlaceHolder("Card title...")
	a.input = input

	addBtn := widget.NewButtonWithIcon("Add", theme.ContentAddIcon(), func() {
		a.addCard()
	})
	addBtn.Importance = widget.HighImportance

	importBtn := widget.NewButtonWithIcon("URL", theme.NavigateNextIcon(), func() {
		a.showImportURLDialog()
	})

	btnRow := container.NewHBox(addBtn, importBtn)
	topBar := container.NewBorder(nil, nil, nil, btnRow, input)

	a.cardList = container.NewVBox()
	scroll := container.NewScroll(a.cardList)

	content := container.NewBorder(topBar, nil, nil, nil, scroll)
	return container.NewTabItemWithIcon("Cards", theme.HomeIcon(), content)
}

func (a *App) buildDetailTab() *container.TabItem {
	placeholder := canvas.NewText("Select a card to view details", colMeta)
	placeholder.Alignment = fyne.TextAlignCenter

	a.detailBox = container.NewVBox(
		layout.NewSpacer(),
		placeholder,
		layout.NewSpacer(),
	)
	scroll := container.NewScroll(a.detailBox)
	return container.NewTabItemWithIcon("Detail", theme.DocumentIcon(), scroll)
}

func (a *App) addCard() {
	text := trimSpace(a.input.Text)
	if text == "" {
		return
	}
	c := &card.Card{
		ID:      fmt.Sprintf("card_%d", time.Now().UnixNano()),
		Title:   text,
		Summary: text,
		Detail:  "Tap edit to add details.",
		Created: time.Now().Format(time.RFC3339),
	}
	a.store.Add(c)
	a.store.Save()
	a.input.SetText("")
	a.refreshList()
}

func (a *App) showImportURLDialog() {
	entry := widget.NewEntry()
	entry.SetPlaceHolder("https://example.com")
	dialog.ShowCustomConfirm("Import Website", "Import", "Cancel", entry, func(ok bool) {
		rawURL := trimSpace(entry.Text)
		if !ok || rawURL == "" {
			return
		}
		if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
			rawURL = "https://" + rawURL
		}
		a.importURL(rawURL)
	}, a.win)
}

func (a *App) importURL(rawURL string) {
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		a.addCardFromURL(rawURL, rawURL, rawURL)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	title := extractTitle(string(body))
	if title == "" {
		title = rawURL
	}
	a.addCardFromURL(title, rawURL, rawURL)
}

func (a *App) addCardFromURL(title, summary, detail string) {
	c := &card.Card{
		ID:      fmt.Sprintf("card_%d", time.Now().UnixNano()),
		Title:   title,
		Summary: summary,
		Detail:  detail,
		Created: time.Now().Format(time.RFC3339),
	}
	a.store.Add(c)
	a.store.Save()
	a.refreshList()
}

func extractTitle(html string) string {
	lower := strings.ToLower(html)
	start := strings.Index(lower, "<title")
	if start == -1 {
		return ""
	}
	closeTag := strings.Index(html[start:], ">")
	if closeTag == -1 {
		return ""
	}
	contentStart := start + closeTag + 1
	end := strings.Index(lower[contentStart:], "</title>")
	if end == -1 {
		return ""
	}
	t := strings.TrimSpace(html[contentStart : contentStart+end])
	return t
}

func (a *App) refreshList() {
	a.cardList.Objects = nil
	cards := a.store.GetAll()
	if len(cards) == 0 {
		msg := canvas.NewText("No cards yet. Add one above.", colMeta)
		msg.Alignment = fyne.TextAlignCenter
		a.cardList.Add(msg)
		return
	}
	for i, c := range cards {
		cc := c
		children := a.store.GetChildren(cc.ID)
		w := newCardTapWidget(a, cc, len(children), i)
		a.cardList.Add(w)
	}
	a.cardList.Refresh()
}

func (a *App) selectCard(c *card.Card) {
	a.selected = c
	a.refreshList()
	a.showDetail()
	a.tabs.SelectIndex(1)
}

func (a *App) showDetail() {
	a.detailBox.Objects = nil
	if a.selected == nil {
		return
	}
	c := a.selected

	title := canvas.NewText(c.Title, cfBlue)
	title.TextSize = 22
	title.TextStyle = fyne.TextStyle{Bold: true}

	createdText := "Created: " + c.Created
	created := canvas.NewText(createdText, colMeta)
	created.TextSize = 12

	sep1 := canvas.NewLine(colDiv)
	sep1.StrokeWidth = 2

	sumH := canvas.NewText("SUMMARY", colMeta)
	sumH.TextSize = 11

	sumV := widget.NewLabel(c.Summary)
	sumV.Wrapping = fyne.TextWrapWord

	sep2 := canvas.NewLine(colDiv)
	sep2.StrokeWidth = 2

	detH := canvas.NewText("DETAIL", colMeta)
	detH.TextSize = 11

	detV := widget.NewLabel(c.Detail)
	detV.Wrapping = fyne.TextWrapWord

	editBtn := widget.NewButtonWithIcon("Edit Detail", theme.DocumentCreateIcon(), func() {
		entry := widget.NewMultiLineEntry()
		entry.SetText(c.Detail)
		dialog.ShowCustomConfirm("Edit Details", "Save", "Cancel", entry, func(ok bool) {
			if ok && entry.Text != "" {
				c.Detail = entry.Text
				a.store.Save()
				a.showDetail()
			}
		}, a.win)
	})
	editBtn.Importance = widget.HighImportance

	sep3 := canvas.NewLine(colDiv)
	sep3.StrokeWidth = 2

	actions := container.NewHBox(
		widget.NewButtonWithIcon("Link Card", theme.NavigateNextIcon(), func() {
			a.showLinkDialog()
		}),
		widget.NewButtonWithIcon("Add Child", theme.ContentAddIcon(), func() {
			a.showAddChildDialog()
		}),
	)

	urlStr := cardURL(c)
	if urlStr != "" {
		openURLBtn := widget.NewButtonWithIcon("Open URL", theme.ComputerIcon(), func() {
			a.showURLPopup(urlStr)
		})
		sepURL := canvas.NewLine(colDiv)
		sepURL.StrokeWidth = 2
		actions = container.NewVBox(actions, sepURL, openURLBtn)
	}

	childBox := a.buildChildBox(c)

	a.detailBox.Add(container.NewPadded(container.NewVBox(
		title,
		created,
		sep1,
		sumH,
		sumV,
		sep2,
		detH,
		detV,
		editBtn,
		sep3,
		actions,
		childBox,
		layout.NewSpacer(),
	)))
	a.detailBox.Refresh()
}

func (a *App) buildChildBox(c *card.Card) *fyne.Container {
	children := a.store.GetChildren(c.ID)
	childBox := container.NewVBox()
	if len(children) > 0 {
		childH := canvas.NewText(
			fmt.Sprintf("CHILDREN (%d)", len(children)),
			colMeta,
		)
		childH.TextSize = 11
		childBox.Add(childH)
		for _, child := range children {
			cc := child
			childBtn := widget.NewButton("↳ "+cc.Title, func() {
				a.selectCard(cc)
			})
			childBtn.Importance = widget.LowImportance
			childBox.Add(childBtn)
		}
	}
	return childBox
}

func (a *App) showLinkDialog() {
	if a.selected == nil {
		return
	}
	cards := a.store.GetAll()
	var names []string
	var cardMap []*card.Card
	for _, c := range cards {
		if c.ID != a.selected.ID {
			names = append(names, c.Title)
			cardMap = append(cardMap, c)
		}
	}
	if len(names) == 0 {
		dialog.ShowInformation("No Cards", "No other cards to link.", a.win)
		return
	}
	sel := widget.NewSelect(names, nil)
	dialog.ShowCustomConfirm("Link Card", "Link", "Cancel", sel, func(ok bool) {
		if ok && sel.Selected != "" {
			for _, c := range cardMap {
				if c.Title == sel.Selected {
					c.ParentID = a.selected.ID
					a.store.Save()
					a.showDetail()
					a.refreshList()
					break
				}
			}
		}
	}, a.win)
}

func (a *App) showAddChildDialog() {
	if a.selected == nil {
		return
	}
	entry := widget.NewEntry()
	entry.SetPlaceHolder("Child card title...")
	dialog.ShowCustomConfirm("New Child Card", "Create", "Cancel", entry, func(ok bool) {
		if ok && entry.Text != "" {
			child := &card.Card{
				ID:       fmt.Sprintf("card_%d", time.Now().UnixNano()),
				Title:    entry.Text,
				Summary:  entry.Text,
				Detail:   "Tap edit to add details.",
				ParentID: a.selected.ID,
				Created:  time.Now().Format(time.RFC3339),
			}
			a.store.Add(child)
			a.store.Save()
			a.showDetail()
			a.refreshList()
		}
	}, a.win)
}

func trimSpace(s string) string {
	return strings.TrimSpace(s)
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func cardURL(c *card.Card) string {
	if isURL(c.Summary) {
		return c.Summary
	}
	if isURL(c.Detail) {
		return c.Detail
	}
	if isURL(c.Title) {
		return c.Title
	}
	return ""
}

func stripHTML(html string) string {
	var buf strings.Builder
	inTag := false
	inEntity := false
	var entity strings.Builder
	for _, r := range html {
		switch {
		case inTag:
			if r == '>' {
				inTag = false
			}
		case r == '<':
			inTag = true
		case r == '&':
			inEntity = true
			entity.Reset()
		case inEntity:
			if r == ';' {
				ent := entity.String()
				switch ent {
				case "amp":
					buf.WriteRune('&')
				case "lt":
					buf.WriteRune('<')
				case "gt":
					buf.WriteRune('>')
				case "nbsp":
					buf.WriteRune(' ')
				default:
					if strings.HasPrefix(ent, "#") {
						var code int
						fmt.Sscanf(ent[1:], "%d", &code)
						if code > 0 {
							buf.WriteRune(rune(code))
						}
					}
				}
				inEntity = false
			} else {
				entity.WriteRune(r)
			}
		default:
			buf.WriteRune(r)
		}
	}
	return buf.String()
}

func (a *App) showURLPopup(urlStr string) {
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(urlStr)
	if err != nil {
		dialog.ShowError(err, a.win)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := trimSpace(stripHTML(string(body)))
	lines := strings.Split(text, "\n")
	var cleaned []string
	for _, l := range lines {
		l = trimSpace(l)
		if l != "" {
			cleaned = append(cleaned, l)
		}
	}
	content := strings.Join(cleaned, "\n")

	contentLabel := widget.NewLabel(content)
	contentLabel.Wrapping = fyne.TextWrapWord

	scroll := container.NewScroll(contentLabel)
	scroll.SetMinSize(fyne.NewSize(600, 400))

	parsed, _ := url.Parse(urlStr)
	openBtn := widget.NewButton("Open in Browser", func() {
		fyne.CurrentApp().OpenURL(parsed)
	})

	popup := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Page Content", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
			openBtn,
		),
		nil, nil, nil,
		scroll,
	)

	dialog.ShowCustomConfirm("URL: "+urlStr, "Close", "", popup, func(bool) {}, a.win)
}

func textColorFor(bg color.Color) color.Color {
	r, g, b, _ := bg.RGBA()
	if r > 63000 && g > 63000 && b > 63000 {
		return color.Black
	}
	luma := 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
	if luma < 38550 {
		return color.White
	}
	return color.Black
}

func cardPreview(c *card.Card) string {
	content := c.Detail
	if content == "" || content == "Tap edit to add details." {
		content = c.Summary
	}
	runes := []rune(content)
	if len(runes) > 100 {
		return string(runes[:100]) + "…"
	}
	return content
}

type cardTapWidget struct {
	widget.BaseWidget
	app        *App
	card       *card.Card
	childCount int
	colorIdx   int
}

func newCardTapWidget(a *App, c *card.Card, cc, ci int) *cardTapWidget {
	w := &cardTapWidget{app: a, card: c, childCount: cc, colorIdx: ci}
	w.ExtendBaseWidget(w)
	return w
}

func (w *cardTapWidget) Tapped(*fyne.PointEvent) {
	w.app.selectCard(w.card)
}

func (w *cardTapWidget) CreateRenderer() fyne.WidgetRenderer {
	hColor := cardColors[w.colorIdx%len(cardColors)]
	headerTextColor := textColorFor(hColor)

	headerBg := canvas.NewRectangle(hColor)
	title := canvas.NewText(w.card.Title, headerTextColor)
	title.TextSize = 14
	title.TextStyle = fyne.TextStyle{Bold: true}
	header := container.NewStack(headerBg, container.NewCenter(container.NewPadded(title)))

	previewBg := canvas.NewRectangle(colPreBg)
	previewStr := cardPreview(w.card)
	previewText := canvas.NewText(previewStr, color.RGBA{0x55, 0x55, 0x66, 0xff})
	previewText.TextSize = 11
	preview := container.NewStack(previewBg, container.NewPadded(previewText))

	bodyBg := canvas.NewRectangle(colGreyBg)
	dateLabel := widget.NewLabel(w.card.Created)
	metaBox := container.NewHBox(dateLabel)
	if w.childCount > 0 {
		chip := canvas.NewText(fmt.Sprintf("%d children", w.childCount), color.RGBA{0x55, 0x55, 0x55, 0xff})
		chip.TextSize = 11
		metaBox = container.NewHBox(dateLabel, widget.NewLabel("  |  "), chip)
	}
	body := container.NewStack(bodyBg, container.NewPadded(metaBox))

	inner := container.NewVBox(header, preview, body)

	blackBg := canvas.NewRectangle(color.Black)
	padded := container.NewPadded(inner)
	card := container.NewStack(blackBg, padded)

	shadowR := canvas.NewRectangle(color.RGBA{0, 0, 0, 0x25})
	shadowB := canvas.NewRectangle(color.RGBA{0, 0, 0, 0x25})
	shadowR.SetMinSize(fyne.NewSize(3, 0))
	shadowB.SetMinSize(fyne.NewSize(0, 3))

	cardWShad := container.NewBorder(nil, shadowB, nil, shadowR, card)
	paddedCard := container.NewPadded(cardWShad)

	return widget.NewSimpleRenderer(paddedCard)
}
