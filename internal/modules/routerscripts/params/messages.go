package params

import "github.com/tikhonp/proxier/internal/platform/i18n"

// Messages are the texts of the findings and the value checks; the module
// root merges them into its catalog.
func Messages() i18n.Messages { return messages }

var messages = i18n.Messages{
	"params.warn.no_block":            {EN: "No PARAMETERS block: the script is versioned and downloadable, with nothing to fill in.", RU: "Нет блока PARAMETERS: у скрипта будут версии и скачивание, но заполнять в нём нечего."},
	"params.warn.end_without_block":   {EN: "Line {line} is # END PARAMETERS, but no # PARAMETERS line comes before it.", RU: "Строка {line} — # END PARAMETERS, но перед ней нет строки # PARAMETERS."},
	"params.warn.no_end":              {EN: "The PARAMETERS block has no # END PARAMETERS line, so it ends at line {line}; the last values read are {names}.", RU: "У блока PARAMETERS нет строки # END PARAMETERS, поэтому он кончается на строке {line}; последние прочитанные значения: {names}."},
	"params.warn.code_in_block":       {EN: "Line {line} inside the PARAMETERS block is neither a comment nor a :local line.", RU: "Строка {line} внутри блока PARAMETERS — не комментарий и не строка :local."},
	"params.warn.annotation_computed": {EN: "{annotation} is above a computed value: there is nothing to fill in.", RU: "{annotation} стоит над вычисляемым значением: заполнять нечего."},
	"params.warn.annotation_orphan":   {EN: "{annotation} belongs to no parameter: a blank line or the end of the block follows it.", RU: "{annotation} не относится ни к одному параметру: за ней пустая строка или конец блока."},
	"params.warn.default_pattern":     {EN: "The default of {name} doesn't match its @pattern.", RU: "Значение {name} по умолчанию не подходит под его @pattern."},
	"params.warn.gone":                {EN: "{name} was in v{version} and is gone here.", RU: "{name} был в v{version}, а здесь его нет."},
	"params.err.second_block":         {EN: "Line {line} is a second # PARAMETERS line before the block's end.", RU: "Строка {line} — вторая строка # PARAMETERS до конца блока."},
	"params.err.unreadable":           {EN: "Line {line}: can't read this :local line.", RU: "Строка {line}: не получается прочитать эту строку :local."},
	"params.err.duplicate":            {EN: "Duplicate parameter {name}.", RU: "Параметр {name} повторяется."},
	"params.err.annotation_unknown":   {EN: "Unknown annotation {annotation}.", RU: "Неизвестная аннотация {annotation}."},
	"params.err.annotation_args":      {EN: "{annotation} takes nothing after it.", RU: "После {annotation} ничего не пишется."},
	"params.err.choices":              {EN: "{annotation} needs values separated by |, none empty or repeated.", RU: "{annotation} требует значений через |, без пустых и повторов."},
	"params.err.pattern_bad":          {EN: "{annotation} isn't a valid regular expression: {error}.", RU: "{annotation} — неверное регулярное выражение: {error}."},
	"params.err.fill_kind":            {EN: "{annotation} needs one of: {kinds}.", RU: "{annotation} требует одно из: {kinds}."},
	"params.err.annotation_twice":     {EN: "{annotation} is on {name} twice.", RU: "{annotation} стоит у {name} дважды."},
	"params.err.fill_twice":           {EN: "@fill {kind} is already on {name}.", RU: "@fill {kind} уже стоит у {name}."},
	"params.err.fill_bare":            {EN: "@fill on {name} needs a quoted default: fills are strings.", RU: "@fill у {name} требует значения в кавычках: подставляются строки."},
	"params.err.fill_choices":         {EN: "{name} can't have both @fill and @choices.", RU: "У {name} не может быть одновременно @fill и @choices."},
	"params.err.default_choice":       {EN: "The default of {name} isn't one of its @choices.", RU: "Значение {name} по умолчанию не входит в его @choices."},

	"params.err.required": {EN: "Required.", RU: "Обязательное поле."},
	"params.err.choice":   {EN: "Pick one of the choices.", RU: "Выберите один из вариантов."},
	"params.err.pattern":  {EN: "Doesn't match the expected format.", RU: "Не подходит под нужный формат."},
	"params.err.control":  {EN: "Must not contain line breaks, tabs or other control characters.", RU: "Не должно быть переводов строки, табуляций и других управляющих символов."},
	"params.err.bare":     {EN: "Must be one word like the default, e.g. 8.", RU: "Должно быть одним словом, как значение по умолчанию, например 8."},
}
