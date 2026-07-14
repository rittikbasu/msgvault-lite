package cmd

// Source-type identifiers stored in sources.source_type and matched against
// when dispatching sync/import logic per account kind.
const (
	sourceTypeGmail    = "gmail"
	sourceTypeIMAP     = "imap"
	sourceTypeMbox     = "mbox"
	sourceTypeTeams    = "teams"
	sourceTypeCalendar = "gcal"
)

// SQLite table names used as structured log keys.
const (
	tableMessages    = "messages"
	tableLabels      = "labels"
	tableAttachments = "attachments"
)

// flagJSON is the name of the boolean --json output flag. It is kept distinct
// from outputFormatJSON (the value accepted by --format) so the flag name and
// the format value can change independently.
const flagJSON = "json"

// cmdUseList is the shared Cobra use/name for list subcommands.
const cmdUseList = "list"

// outputFormatJSON is the "json" value accepted by the --format flag.
const outputFormatJSON = "json"

// keyEmail is the map/log field key carrying an account or address email.
const keyEmail = "email"
