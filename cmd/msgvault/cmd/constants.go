package cmd

// Source-type identifier stored in sources.source_type.
const (
	sourceTypeGmail = "gmail"
)

// SQLite table names used as structured log keys.
const (
	tableMessages    = "messages"
	tableLabels      = "labels"
	tableAttachments = "attachments"
)

// flagJSON is the name of the boolean --json output flag.
const flagJSON = "json"

// cmdUseList is the shared Cobra use/name for list subcommands.
const cmdUseList = "list"

const cmdAddAccount = "add-account"

// keyEmail is the map/log field key carrying an account or address email.
const keyEmail = "email"
