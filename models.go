package main

// These DTOs are the entire output allowlist. In particular, email, Telegram
// account data, upstream metadata and unknown nested fields are never forwarded.
type User struct {
	ID                  string  `json:"id"`
	Slug                string  `json:"slug"`
	FullName            string  `json:"full_name"`
	Avatar              *string `json:"avatar"`
	Bio                 *string `json:"bio,omitempty"`
	Company             *string `json:"company,omitempty"`
	Position            *string `json:"position,omitempty"`
	City                *string `json:"city,omitempty"`
	Country             *string `json:"country,omitempty"`
	Upvotes             int     `json:"upvotes"`
	CreatedAt           string  `json:"created_at"`
	MembershipStartedAt string  `json:"membership_started_at"`
	MembershipExpiresAt string  `json:"membership_expires_at"`
	ModerationStatus    string  `json:"moderation_status"`
	PaymentStatus       string  `json:"payment_status"`
	IsActiveMember      bool    `json:"is_active_member"`
}
type userResponse struct {
	User User `json:"user"`
}

// Pointers distinguish requested empty collections from unrequested sections.
type getUserResponse struct {
	User         *User          `json:"user,omitempty"`
	Tags         *tagGroups     `json:"tags,omitempty"`
	Badges       *[]badge       `json:"user_badges,omitempty"`
	Achievements *[]achievement `json:"user_achievements,omitempty"`
}
type Tag struct {
	Code string `json:"code"`
	Name string `json:"name"`
}
type tagGroups struct {
	Club        []Tag `json:"club"`
	Tech        []Tag `json:"tech"`
	Hobbies     []Tag `json:"hobbies"`
	Personal    []Tag `json:"personal"`
	Collectible []Tag `json:"collectible"`
	Other       []Tag `json:"other"`
}
type tagsResponse struct {
	Tags tagGroups `json:"tags"`
}
type idReference struct {
	ID string `json:"id"`
}
type badge struct {
	Badge struct {
		Code        string  `json:"code"`
		Title       string  `json:"title"`
		Description *string `json:"description,omitempty"`
		CreatedAt   string  `json:"created_at"`
	} `json:"badge"`
	FromUser  *User        `json:"from_user"`
	CreatedAt string       `json:"created_at"`
	Post      *idReference `json:"post,omitempty"`
	Comment   *idReference `json:"comment,omitempty"`
	Note      *string      `json:"note,omitempty"`
}
type badgesResponse struct {
	Badges []badge `json:"user_badges"`
}
type achievement struct {
	Achievement struct {
		Code        string  `json:"code"`
		Name        string  `json:"name"`
		Image       *string `json:"image,omitempty"`
		Description *string `json:"description,omitempty"`
		Style       *string `json:"style,omitempty"`
	} `json:"achievement"`
	CreatedAt string `json:"created_at"`
}
type achievementsResponse struct {
	Achievements []achievement `json:"user_achievements"`
}
type author struct {
	Name   string  `json:"name"`
	URL    string  `json:"url"`
	Avatar *string `json:"avatar"`
}
type postInfo struct {
	Type             string   `json:"type"`
	Slug             string   `json:"slug"`
	Coauthors        []string `json:"coauthors,omitempty"`
	CommentCount     int      `json:"comment_count"`
	ViewCount        int      `json:"view_count"`
	Upvotes          int      `json:"upvotes"`
	IsPublic         bool     `json:"is_public"`
	IsCommentable    bool     `json:"is_commentable"`
	ContentTruncated bool     `json:"content_truncated,omitempty"`
	ContentFullChars int      `json:"content_full_chars,omitempty"`
}
type Post struct {
	ID            string   `json:"id"`
	URL           string   `json:"url"`
	Title         string   `json:"title"`
	ContentText   string   `json:"content_text"`
	DatePublished *string  `json:"date_published"`
	DateModified  *string  `json:"date_modified"`
	Authors       []author `json:"authors"`
	Club          postInfo `json:"_club"`
}
type postResponse struct {
	Post Post `json:"post"`
}
type comment struct {
	ID        string          `json:"id"`
	URL       string          `json:"url"`
	Text      string          `json:"text"`
	Author    User            `json:"author"`
	ReplyToID *string         `json:"reply_to_id"`
	Upvotes   int             `json:"upvotes"`
	CreatedAt string          `json:"created_at"`
	Club      *contentPreview `json:"_club,omitempty"`
}
type contentPreview struct {
	ContentTruncated bool `json:"content_truncated,omitempty"`
	ContentFullChars int  `json:"content_full_chars,omitempty"`
}
type commentsResponse struct {
	Comments []comment `json:"comments"`
}
type feedResponse struct {
	Version     string `json:"version"`
	Title       string `json:"title"`
	HomePageURL string `json:"home_page_url"`
	FeedURL     string `json:"feed_url"`
	NextURL     string `json:"next_url,omitempty"`
	Items       []Post `json:"items"`
}
type userMatch struct {
	Slug     string  `json:"slug"`
	FullName string  `json:"full_name"`
	Avatar   *string `json:"avatar"`
}
type searchUsersResponse struct {
	Users []userMatch `json:"users"`
}
type searchResult struct {
	Type    string   `json:"type"`
	Post    *Post    `json:"post,omitempty"`
	Comment *comment `json:"comment,omitempty"`
	User    *User    `json:"user,omitempty"`
}
type searchResponse struct {
	Query    string         `json:"query"`
	Type     *string        `json:"type"`
	Ordering string         `json:"ordering"`
	Page     int            `json:"page"`
	Pages    int            `json:"pages"`
	Results  []searchResult `json:"results"`
}
type tagMatch struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Group string `json:"group"`
	Color string `json:"color"`
}
type searchTagsResponse struct {
	Tags []tagMatch `json:"tags"`
}
type markdownResponse struct {
	Markdown string `json:"markdown"`
}
