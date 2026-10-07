# Router script parameters are the literal `:local` values of the PARAMETERS block

Proxier fills in `fresh-router.rsc` per router without any template syntax. The parameters are the `:local <name> <literal>` lines between `# PARAMETERS` and `# END PARAMETERS`. Descriptions come from the comments directly above each line, and optional annotations (`# @secret`, `# @fill subscription-link`, …) are comments too. Generating rewrites only those literals. The script therefore stays a valid RouterOS file that can still be imported by hand, read in a diff, and edited in any editor. A Go-template version of it would be unimportable until rendered.

## Considered options

- **Go template placeholders** (`{{ .LanNet }}`): explicit and flexible, but the file is no longer RouterOS until rendered, and every edit has to keep two syntaxes apart.
- **Detecting the end of the block from headings**: the existing script's comments start with capitals too (`# LAN /24…`, `# DOH_FORWARDER…`), so heading detection would cut the block short. Hence the explicit end marker.

## Consequences

- Values that aren't literals (expressions such as `($containerNet . ".2")`) are computed, not parameters.
- The existing script needs one added line, `# END PARAMETERS`, plus optional annotations, before it is published in Proxier.
