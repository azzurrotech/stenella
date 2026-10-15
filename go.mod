module azzurrotech/stenella

go 1.22

require azzurrotech/atp v0.0.0

require (
	azzurrotech/pod v0.0.0 // indirect
	azzurrotech/shepherd v0.0.0 // indirect
	azzurrotech/song v0.0.0 // indirect
)

replace azzurrotech/atp => ./atp

replace azzurrotech/pod => ./atp/pod

replace azzurrotech/shepherd => ./atp/shepherd

replace azzurrotech/song => ./atp/song
