  // renderItemAnnotations displays the comments and links for an item
  function renderItemAnnotations(it) {
    const fragments = [];
    
    // Display comments
    if (it.comments && it.comments.length > 0) {
      fragments.push(el("div", { class: "annotations" },
        el("h4", { text: "Comments (" + it.comments.length + ")" }),
        el("ul", { class: "comments" },
          it.comments.map((c) => 
            el("li", { class: "comment" },
              el("span", { class: "author", text: c.author || "anonymous" }),
              ": ",
              el("span", { class: "body", text: c.body }),
              el("span", { class: "muted", text: " " + S.timeAgo(c.created) })
            )
          )
        )
      ));
    }
    
    // Display links
    if (it.links && it.links.length > 0) {
      fragments.push(el("div", { class: "annotations" },
        el("h4", { text: "Links (" + it.links.length + ")" }),
        el("ul", { class: "links" },
          it.links.map((l) => 
            el("li", { class: "link" },
              "Related to ",
              el("a", { href: "#/feed?item=" + encodeURIComponent(l.to), text: "this item" }),
              " (" + (l.relation || "related") + ")"
            )
          )
        )
      ));
    }
    
    return fragments;
  }

  // timelineInto renders items into a list. It is shared by the feed, the
  // search results and the briefing, so every view shows the same record.
  function timelineInto(list, items, rerender) {
    clear(list);
    if (!items.length) {
      list.appendChild(el("li", { class: "muted", text: "nothing collected yet — run a collection from Sources" }));
      return;
    }
    for (const it of items) {
      const annotations = renderItemAnnotations(it);
      list.appendChild(el("li", {},
        el("div", { class: "head" },
          el("span", { class: "title", text: it.title || "(untitled)" }),
          el("span", { class: "muted", text: S.timeAgo(it.published || it.fetched) })),
        el("div", { class: "badges" }, itemBadges(it)),
        it.summary ? el("p", { text: it.summary }) : null,
        ...annotations,
        itemActions(it, rerender)));
    }
  }

