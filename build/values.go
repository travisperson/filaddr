package build

// StoredMessages in the number of messages that will be pushed into the redis
// list for a given address. This is the maximum.
const StoredMessages = 128

// FeedLength is the number of messages that will be returned to the user when
// their feed is requested. This is the maximum.
const FeedLength = 16
