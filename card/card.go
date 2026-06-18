package card

import "encoding/xml"

type Card struct {
	XMLName  xml.Name `xml:"card"`
	ID       string   `xml:"id"`
	Title    string   `xml:"title"`
	Summary  string   `xml:"summary"`
	Detail   string   `xml:"detail"`
	Children []*Card  `xml:"children>card,omitempty"`
	ParentID string   `xml:"parent_id,omitempty"`
	Created  string   `xml:"created"`
}

type Deck struct {
	XMLName xml.Name `xml:"deck"`
	Cards   []*Card  `xml:"cards>card"`
}
