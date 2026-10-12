package routerscripts

import "github.com/tikhonp/proxier/internal/platform/i18n"

// agentMessages are the texts of handing a draft to an agent; the shared
// ones (agent.*) are the platform's. What the agent itself reads (the
// prompt, the API's answers) is English and lives in routerscripts/agent.
var agentMessages = i18n.Messages{
	"job.routerscripts.agent_sessions":             {EN: "Close expired agent sessions", RU: "Закрыть истёкшие сеансы агентов"},
	"job.routerscripts.agent_sessions.step.expire": {EN: "Close expired sessions", RU: "Закрыть истёкшие сеансы"},

	"event.routerscript.agent_session_opened":       {EN: "Handed the draft of {subject} to an agent", RU: "Черновик {subject} передан агенту"},
	"event.routerscript.agent_session_closed":       {EN: "Ended the agent session on {subject} ({reason})", RU: "Сеанс агента на {subject} завершён ({reason})"},
	"notify.routerscript.agent_session_opened":      {EN: "An agent can edit the draft of {subject}", RU: "Агент может править черновик {subject}"},
	"notify.routerscript.agent_session_opened.body": {EN: "Anyone holding the copied prompt can, until the session ends.", RU: "Любой, у кого есть скопированная подсказка, — пока сеанс не закончится."},

	"rscripts.agent.ctx.check":     {EN: "The check of the draft (what Publish would refuse or warn about) and the parameters reference are always included.", RU: "Проверка черновика (что «Опубликовать» запретит или о чём предупредит) и справочник параметров доступны всегда."},
	"rscripts.agent.ctx.no_base":   {EN: "The draft was never published, so there is no diff to include.", RU: "Черновик не публиковался, поэтому отличий от версии нет."},
	"rscripts.agent.hidden":        {EN: "The agent reads these defaults as MASKED-BY-PROXIER and never sees them: {names}.", RU: "Эти значения по умолчанию агент видит как MASKED-BY-PROXIER и никогда не получает: {names}."},
	"rscripts.agent.can.1":         {EN: "read and replace this draft", RU: "читать и заменять этот черновик"},
	"rscripts.agent.can.2":         {EN: "check it as Publish would", RU: "проверять его так же, как «Опубликовать»"},
	"rscripts.agent.can.3":         {EN: "read the context above", RU: "читать контекст выше"},
	"rscripts.agent.cant.1":        {EN: "publish or discard", RU: "публиковать или отбрасывать"},
	"rscripts.agent.cant.2":        {EN: "generate for a router or create fetch URLs", RU: "делать генерации и ссылки для скачивания"},
	"rscripts.agent.cant.3":        {EN: "see a secret default or a subscription link", RU: "видеть секретные значения и ссылку подписки"},
	"rscripts.agent.cant.4":        {EN: "touch anything else", RU: "трогать что-либо ещё"},
	"rscripts.agent.to_script":     {EN: "Back to the script", RU: "К скрипту"},
	"rscripts.agent.to_editor":     {EN: "Open the draft", RU: "Открыть черновик"},
	"rscripts.agent.secret.revoke": {EN: "To end it sooner, revoke access on the script page.", RU: "Чтобы закончить раньше, отзовите доступ на странице скрипта."},
	"rscripts.agent.band.counts":   {EN: "{saves} saves", RU: "сохранений: {saves}"},
	"rscripts.agent.band.revoked":  {EN: "The agent's access was revoked.", RU: "Доступ агента отозван."},
	"rscripts.agent.stale.title":   {EN: "The draft changed since you opened it (an agent or another tab saved it).", RU: "Черновик изменился, пока он был открыт (его сохранил агент или другая вкладка)."},
}
