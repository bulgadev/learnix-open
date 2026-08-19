// Package quizgen builds brutal ENEM/vestibular quizzes through a
// web-researched three-stage pipeline: research, author, review. The pipeline
// never sees the student's study material — only topic, feedback and count.
package quizgen

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"learnix/internal/ai"
	"learnix/internal/elements"
	"learnix/internal/session"
	"learnix/internal/websearch"
)

// Spec describes the quiz to generate.
type Spec struct {
	Topic    string
	Feedback string
	Count    int
	// Web runs the research stage; false (debug only) skips it and authors
	// questions from model knowledge alone.
	Web bool
	// PrevStems lists question stems the learner already saw in earlier
	// attempts; the author must not repeat or disguise them.
	PrevStems []string
	// ResearchModel/AuthorModel/ReviewModel optionally override the model for
	// one pipeline stage (empty = cfg.Model). Lets the deployment run the
	// expensive research on a strong model and author/review on a cheap one.
	ResearchModel string
	AuthorModel   string
	ReviewModel   string
}

// Progress is one pipeline status update. Metrics are cumulative for the job
// and are attached by GenerateWithTrace before reaching the caller.
type ProgressMetrics struct {
	Current     int
	Total       int
	Sources     int
	Searches    int
	Pages       int
	ModelCalls  int
	Tokens      int64
	Attempt     int
	MaxAttempts int
}

type Progress struct {
	Stage   string
	Level   string
	Message string
	Metrics *ProgressMetrics
}

const maxAttempts = 3

type parsedQuestions struct {
	Questions     []session.Question
	ElementIssues []string
}

// Generate returns the questions, sources and aggregate provider usage. The
// richer GenerateWithTrace variant also exposes the research and evaluation
// criteria persisted with a quiz.
func Generate(ctx context.Context, c *ai.Client, cfg session.Config, web *websearch.Client, spec Spec, onProgress func(Progress)) ([]session.Question, []websearch.Result, ai.Usage, error) {
	qs, sources, trace, err := GenerateWithTrace(ctx, c, cfg, web, spec, onProgress)
	return qs, sources, ai.Usage{Prompt: trace.TokenUsage.PromptTokens, Completion: trace.TokenUsage.CompletionTokens}, err
}

// GenerateWithTrace runs the research/author/review pipeline and returns a
// user-visible record of the material and criteria used to build the quiz.
func GenerateWithTrace(ctx context.Context, c *ai.Client, cfg session.Config, web *websearch.Client, spec Spec, onProgress func(Progress)) ([]session.Question, []websearch.Result, session.QuizTrace, error) {
	if onProgress == nil {
		onProgress = func(Progress) {}
	}
	var brief string
	var sources []websearch.Result
	var usage session.TokenUsage
	trace := session.QuizTrace{
		Topic: spec.Topic, Feedback: spec.Feedback, Count: spec.Count,
		Web: spec.Web, Model: cfg.Model, Sources: nil,
		AuthorCriteria: authorCriteria(), ReviewCriteria: reviewCriteria(),
		EvaluationCriteria: evaluationCriteria(),
	}
	metrics := &ProgressMetrics{Total: spec.Count, MaxAttempts: maxAttempts}
	report := func(p Progress) {
		if p.Metrics != nil {
			copy := *p.Metrics
			metrics = &copy
		}
		p.Metrics = metrics
		onProgress(p)
	}
	if spec.Web {
		var err error
		var researchUsage session.TokenUsage
		brief, sources, researchUsage, err = research(ctx, c, stageConfig(cfg, spec.ResearchModel), web, spec, report)
		usage.Add(researchUsage)
		if err != nil {
			trace.TokenUsage = usage
			return nil, sources, trace, err
		}
	} else {
		report(Progress{Stage: "research", Message: "Modo depuração: sem pesquisa web."})
		brief = "(Sem pesquisa web: baseie-se no seu conhecimento profundo do estilo e das bancas reais do ENEM/vestibulares.)"
	}
	authorCfg := stageConfig(cfg, spec.AuthorModel)
	reviewCfg := stageConfig(cfg, spec.ReviewModel)
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		metrics.Attempt = attempt + 1
		report(Progress{Stage: "author", Message: fmt.Sprintf("Preparando a tentativa %d/%d...", attempt+1, maxAttempts)})
		qs, authorUsage, authorIssues, aerr := author(ctx, c, authorCfg, spec, brief, report)
		usage.Add(authorUsage)
		metrics.ModelCalls++
		metrics.Tokens = int64(usage.Total())
		if aerr != nil {
			lastErr = aerr
			report(Progress{Stage: "author", Level: "error", Message: fmt.Sprintf("Autoria falhou na tentativa %d/%d: %s", attempt+1, maxAttempts, progressError(aerr))})
			continue
		}
		reportElementIssues(report, "author", authorIssues)
		metrics.Current = len(qs)
		metrics.Tokens = int64(usage.Total())
		report(Progress{Stage: "author", Message: fmt.Sprintf("%d/%d questões criadas; iniciando revisão.", len(qs), spec.Count)})
		final, reviewUsage, reviewIssues, rerr := review(ctx, c, reviewCfg, spec, sources, qs, report)
		usage.Add(reviewUsage)
		metrics.ModelCalls++
		metrics.Tokens = int64(usage.Total())
		if rerr != nil {
			// The authored set is structurally fine; only the review failed.
			// Re-run review once on the same draft before burning a new author
			// call — the most expensive failure mode of the loop.
			report(Progress{Stage: "review", Level: "warning", Message: fmt.Sprintf("Revisão falhou na tentativa %d/%d: %s; revisando o mesmo rascunho outra vez.", attempt+1, maxAttempts, progressError(rerr))})
			var retryUsage session.TokenUsage
			final, retryUsage, reviewIssues, rerr = review(ctx, c, reviewCfg, spec, sources, qs, report)
			usage.Add(retryUsage)
			metrics.ModelCalls++
			metrics.Tokens = int64(usage.Total())
		}
		if rerr != nil {
			lastErr = rerr
			report(Progress{Stage: "review", Level: "error", Message: fmt.Sprintf("Revisão falhou na tentativa %d/%d: %s", attempt+1, maxAttempts, progressError(rerr))})
			continue
		}
		reportElementIssues(report, "review", reviewIssues)
		final = preserveAuthorElements(qs, final)
		metrics.Current = len(final)
		metrics.Tokens = int64(usage.Total())
		report(Progress{Stage: "review", Message: fmt.Sprintf("%d/%d questões revisadas.", len(final), spec.Count)})
		if verr := validate(final, spec.Count); verr != nil {
			lastErr = verr
			report(Progress{Stage: "validate", Level: "error", Message: fmt.Sprintf("Validação falhou na tentativa %d/%d: %s", attempt+1, maxAttempts, progressError(verr))})
			continue
		}
		trace.ResearchBrief = brief
		trace.Sources = references(sources)
		trace.TokenUsage = usage
		return final, sources, trace, nil
	}
	trace.ResearchBrief = brief
	trace.Sources = references(sources)
	trace.TokenUsage = usage
	return nil, sources, trace, fmt.Errorf("não consegui gerar questões válidas: %v", lastErr)
}

func reportElementIssues(report func(Progress), stage string, issues []string) {
	if len(issues) == 0 {
		return
	}
	report(Progress{
		Stage:   stage,
		Level:   "warning",
		Message: fmt.Sprintf("%d elemento(s) estruturado(s) inválido(s) foram removidos; o quiz continuará sem refazer tudo.", len(issues)),
	})
}

func preserveAuthorElements(authored, reviewed []session.Question) []session.Question {
	if len(authored) != len(reviewed) {
		return reviewed
	}
	for i := range reviewed {
		reviewed[i].Elements = append([]elements.Element(nil), authored[i].Elements...)
	}
	return reviewed
}

func progressError(err error) string {
	if err == nil {
		return "erro desconhecido"
	}
	message := strings.Join(strings.Fields(err.Error()), " ")
	if len([]rune(message)) > 240 {
		return string([]rune(message)[:240]) + "..."
	}
	return message
}

func references(sources []websearch.Result) []session.Reference {
	out := make([]session.Reference, 0, len(sources))
	for _, source := range sources {
		out = append(out, session.Reference{Title: source.Title, URL: source.URL, Snippet: source.Snippet})
	}
	return out
}

// stageConfig returns cfg with the model overridden for one pipeline stage; an
// empty override keeps the session default.
func stageConfig(cfg session.Config, model string) session.Config {
	if model == "" {
		return cfg
	}
	cfg.Model = model
	return cfg
}

func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// sourceList renders the compact grounding block for the review prompt: one
// "Title (URL)" line per distinct source, capped so a long research session
// cannot bloat the review call.
func sourceList(sources []websearch.Result) string {
	seen := make(map[string]bool, len(sources))
	var b strings.Builder
	count := 0
	for _, source := range sources {
		if count >= 20 {
			break
		}
		if source.URL == "" || seen[source.URL] {
			continue
		}
		seen[source.URL] = true
		title := strings.TrimSpace(source.Title)
		if title == "" {
			title = source.URL
		}
		b.WriteString("- " + truncateRunes(title, 100) + " (" + source.URL + ")\n")
		count++
	}
	return b.String()
}

func authorCriteria() []string {
	return []string{
		"Usar somente o tema, o feedback e o briefing de pesquisa; o material privado do aluno não entra na autoria.",
		"Criar exatamente cinco alternativas plausíveis, com distratores baseados em erros conceituais reais.",
		"Preferir situações e níveis de dificuldade observados em provas reais, evitando memorização trivial.",
		"Usar elementos estruturados quando uma tabela ou relação hierárquica trouxer valor; caso contrário, usar Markdown comum.",
	}
}

func reviewCriteria() []string {
	return []string{
		"Verificar o gabarito e eliminar qualquer ambiguidade ou segunda alternativa defensável.",
		"Endurecer distratores fracos e substituir questões fáceis demais.",
		"Preservar exatamente os elementos estruturados já validados na autoria.",
	}
}

func evaluationCriteria() []string {
	return []string{
		"Acerto sozinho é evidência de desempenho nesta questão, não prova definitiva de domínio.",
		"Acerto com confiança baixa é sinal de possível chute e deve ser revisado.",
		"Erro com confiança alta é sinal para revisar conceito ou atenção; a IA não distingue os dois sozinha.",
		"Erro com confiança baixa indica incerteza esperada, sem concluir por que o aluno errou.",
		"O aluno pode corrigir o diagnóstico após o resultado marcando chute, desatenção, falta de conhecimento ou outro motivo.",
	}
}

// webTools are the only tools the research stage may use: plain web access,
// never the study's files.
func webTools() []ai.Tool {
	return []ai.Tool{
		{Type: "function", Function: ai.ToolDef{
			Name:        "search_web",
			Description: "Pesquisa na internet e retorna resultados com título, URL e trecho do conteúdo.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "Termos da pesquisa"},
				},
				"required": []string{"query"},
			},
		}},
		{Type: "function", Function: ai.ToolDef{
			Name:        "fetch_url",
			Description: "Lê o conteúdo de uma página web a partir da URL.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{"type": "string", "description": "URL da página"},
				},
				"required": []string{"url"},
			},
		}},
	}
}

// research streams a tool loop that gathers real ENEM/FUVEST/UNICAMP questions
// about the topic and returns the final research brief plus all sources.
func research(ctx context.Context, c *ai.Client, cfg session.Config, web *websearch.Client, spec Spec, onProgress func(Progress)) (string, []websearch.Result, session.TokenUsage, error) {
	metrics := &ProgressMetrics{Total: spec.Count, MaxAttempts: maxAttempts}
	emit := func(level, message string) {
		copy := *metrics
		onProgress(Progress{Stage: "research", Level: level, Message: message, Metrics: &copy})
	}
	emit("", "Pesquisando questões reais de vestibulares sobre o tema...")
	var sources []websearch.Result
	var usage session.TokenUsage
	exec := func(name, argsJSON string) (string, string, error) {
		switch name {
		case "search_web":
			var args struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil || args.Query == "" {
				return "", "", fmt.Errorf("parâmetro query inválido")
			}
			emit("", "Buscando: "+args.Query)
			res, err := web.Search(ctx, args.Query)
			if err != nil {
				emit("error", "Busca falhou: "+progressError(err))
				return "", "", err
			}
			metrics.Searches++
			sources = append(sources, res...)
			metrics.Sources = len(sources)
			emit("", fmt.Sprintf("Busca concluída: %d resultados; %d fontes acumuladas.", len(res), len(sources)))
			b, err := json.Marshal(res)
			if err != nil {
				return "", "", err
			}
			return string(b), fmt.Sprintf("pesquisou: %s (%d resultados)", args.Query, len(res)), nil
		case "fetch_url":
			var args struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil || args.URL == "" {
				return "", "", fmt.Errorf("parâmetro url inválido")
			}
			host := args.URL
			if u, perr := url.Parse(args.URL); perr == nil && u.Host != "" {
				host = u.Host
			}
			emit("", "Lendo página: "+host)
			text, err := web.ExtractFocused(ctx, args.URL, spec.Topic)
			if err != nil {
				emit("error", "Leitura falhou: "+progressError(err))
				return "", "", err
			}
			metrics.Pages++
			emit("", fmt.Sprintf("Página lida: %s (%d páginas).", host, metrics.Pages))
			return text, "leu: " + host, nil
		}
		return "", "", fmt.Errorf("ferramenta desconhecida: %s", name)
	}

	msgs := []ai.Message{
		{Role: "system", Content: "Você é um pesquisador especialista em vestibulares brasileiros (ENEM, FUVEST, UNICAMP e afins). " +
			"Use search_web e fetch_url para encontrar questões REAIS já aplicadas nessas provas. " +
			"Não invente questões nem fontes; só relate o que efetivamente encontrar nas páginas lidas."},
		{Role: "user", Content: "Pesquise questões reais do ENEM, da FUVEST, da UNICAMP e de outros vestibulares sobre: \"" + spec.Topic + "\".\n" +
			"Consulte algumas fontes (provas anteriores, bancos de questões, resoluções comentadas) com search_web e fetch_url.\n" +
			"Ao final, responda com um briefing contendo:\n" +
			"1. Os fatos e padrões relevantes encontrados, identificando cada fonte como S1, S2 etc.; não transcreva questões inteiras.\n" +
			"2. Os temas e abordagens recorrentes sobre o assunto.\n" +
			"3. Os padrões de distratores usados pelas bancas (erros conceituais comuns que as alternativas erradas exploram).\n" +
			"4. As características de dificuldade típicas dessas questões."},
	}
	full, _, toolUsage, err := c.StreamWithTools(ctx, cfg, msgs, webTools(), exec,
		func(string) {},
		func(ev ai.Event) {
			switch ev.Type {
			case "tool":
				emit("", "IA solicitou: "+ev.Name+"...")
			case "tool_result":
				emit("", ev.Summary)
			case "note":
				emit("", ev.Text)
			case "usage":
				if ev.Usage != nil {
					usage.Add(session.TokenUsage{PromptTokens: ev.Usage.Prompt, CompletionTokens: ev.Usage.Completion})
					metrics.ModelCalls++
					metrics.Tokens = int64(usage.Total())
					emit("", fmt.Sprintf("Resposta da pesquisa recebida (%d tokens acumulados).", metrics.Tokens))
				}
			}
		})
	usage = session.TokenUsage{PromptTokens: toolUsage.Prompt, CompletionTokens: toolUsage.Completion}
	if metrics.ModelCalls == 0 {
		metrics.ModelCalls = 1
	}
	if metrics.Tokens != int64(usage.Total()) {
		metrics.Tokens = int64(usage.Total())
		emit("", fmt.Sprintf("Pesquisa concluída (%d tokens acumulados).", metrics.Tokens))
	}
	if err != nil {
		return "", nil, usage, err
	}
	if strings.TrimSpace(full) == "" {
		return "", nil, usage, fmt.Errorf("a pesquisa não retornou nenhum briefing")
	}
	if len(sources) == 0 {
		return "", nil, usage, fmt.Errorf("a pesquisa não consultou nenhuma fonte na web")
	}
	return full, sources, usage, nil
}

// questionsSchema is the JSON shape both author and review must produce.
const questionsSchema = `{"questions":[{"text":"...","context":"...","elements":[],"skill":"...","difficulty":"hard","options":["a","b","c","d","e"],"correct":0,"explanation":"..."}]}`

// author writes the first question set from topic, feedback and the research
// brief only — it has no access to the student's study material.
func author(ctx context.Context, c *ai.Client, cfg session.Config, spec Spec, brief string, onProgress func(Progress)) ([]session.Question, session.TokenUsage, []string, error) {
	onProgress(Progress{Stage: "author", Message: fmt.Sprintf("Elaborando %d questões brutais no estilo ENEM...", spec.Count)})
	var b strings.Builder
	b.WriteString("Você NÃO tem acesso ao material de estudo do aluno (arquivos, notas ou histórico de conversa). " +
		"Baseie-se SOMENTE no tema, no feedback do aluno e no briefing de pesquisa abaixo.\n\n")
	b.WriteString("Tema: \"" + spec.Topic + "\"\n")
	if spec.Feedback != "" {
		b.WriteString("O aluno errou anteriormente pontos sobre: " + spec.Feedback + ". Reforce esses pontos nas questões.\n")
	}
	if len(spec.PrevStems) > 0 {
		b.WriteString("\nNÃO repita nem disfarce estas questões que o aluno já respondeu em tentativas anteriores — mesmo tema é bem-vindo, mas enunciados e alternativas devem ser novos:\n")
		for _, stem := range spec.PrevStems {
			b.WriteString("- " + truncateRunes(strings.TrimSpace(stem), 120) + "\n")
		}
		b.WriteString("Cubra subtemas e habilidades distintos entre as questões desta prova.\n")
	}
	b.WriteString("\nBriefing de pesquisa com questões reais de vestibulares:\n" + brief + "\n\n")
	b.WriteString("Política de origem das questões: prefira adaptar/remixar as questões reais do briefing; " +
		"pode reutilizar na íntegra uma questão real excelente; crie do zero apenas para preencher lacunas de cobertura.\n")
	b.WriteString("Elabore EXATAMENTE " + fmt.Sprint(spec.Count) + " questões ORIGINAIS e brutais, em estilo ENEM autêntico:\n")
	b.WriteString("- \"context\": texto de apoio (documento, tabela, situação) como nas provas reais.\n")
	b.WriteString("- \"elements\": array opcional de elementos estruturados. Use type=table para dados tabulares e type=mind_map para relações hierárquicas; use [] quando não ajudarem. Uma tabela deve ter 1-12 colunas, 1-80 linhas e TODA linha deve ter exatamente o mesmo número de células das colunas. Todos os títulos, cabeçalhos e valores devem ser strings; nunca use null, objetos ou arrays dentro de uma célula.\n")
	b.WriteString("- Elementos estruturados são auxiliares: nunca esconda neles uma informação indispensável que não esteja também no context ou no enunciado.\n")
	b.WriteString("- \"skill\": a habilidade/conceito principal avaliado; \"difficulty\": easy, medium ou hard.\n")
	b.WriteString("- Use Markdown dentro de context quando houver parágrafos, listas ou quebras de linha. Nunca compacte uma tabela em texto separado por barras verticais: use elements para tabelas estruturadas.\n")
	b.WriteString("- \"text\": enunciado claro e exigente.\n")
	b.WriteString("- \"options\": EXATAMENTE 5 alternativas plausíveis; os distratores devem explorar equívocos reais de estudantes.\n")
	b.WriteString("- \"correct\": índice (0 a 4) da alternativa correta.\n")
	b.WriteString("- \"explanation\": explicação do gabarito e de por que cada distrator parece tentador.\n")
	b.WriteString("Dificuldade alta: nada de questões óbvias ou de memorização trivial.\n")
	b.WriteString("\nResponda SOMENTE com JSON válido no formato:\n" + questionsSchema)

	// Temperature 0.7 (not 0.9): ENEM-style authoring needs variety, but the
	// dominant failure at 0.9 was JSON drift triggering full retry cascades.
	content, usage, err := c.CompleteUsage(ctx, cfg, []ai.Message{
		{Role: "system", Content: "Você é um elaborador de questões do ENEM e vestibulares brasileiros, famoso por questões difíceis e sem ambiguidade. Responda apenas com JSON válido."},
		{Role: "user", Content: b.String()},
	}, 0.7, true)
	if err != nil {
		return nil, usage, nil, err
	}
	parsed, err := parseQuestionsRecoveringElements(content)
	if err != nil {
		return nil, usage, nil, err
	}
	return parsed.Questions, usage, parsed.ElementIssues, nil
}

// review is the adversarial pass: verify answer keys, kill ambiguity, harden
// weak distractors and reject easy questions. It receives the research sources
// (not the full brief again) so it can fact-check without paying the brief's
// prompt tokens twice.
func review(ctx context.Context, c *ai.Client, cfg session.Config, spec Spec, sources []websearch.Result, qs []session.Question, onProgress func(Progress)) ([]session.Question, session.TokenUsage, []string, error) {
	onProgress(Progress{Stage: "review", Message: "Revisão adversária: gabaritos, ambiguidades e distratores..."})
	qb, err := json.Marshal(qs)
	if err != nil {
		return nil, session.TokenUsage{}, nil, err
	}
	var b strings.Builder
	b.WriteString("Você é um revisor adversário de questões de vestibular. Receba o rascunho abaixo e:\n")
	b.WriteString("1. Verifique o gabarito de cada questão (corrija \"correct\" e \"explanation\" se estiverem errados).\n")
	b.WriteString("2. Elimine toda ambiguidade: nenhuma questão pode ter mais de uma alternativa defensável.\n")
	b.WriteString("3. Endureça distratores fracos (óbvios, absurdos ou de tamanho muito distinto).\n")
	b.WriteString("4. Rejeite questões fáceis, substituindo-as por versões realmente difíceis.\n")
	b.WriteString("5. Não altere o campo elements: ele já foi validado na autoria e deve ser preservado exatamente, sem criar, remover ou reescrever tabelas/mapas.\n")
	b.WriteString("6. Preserve quebras de linha e Markdown úteis no context; não transforme tabelas estruturadas em texto corrido. Preserve ou corrija skill e difficulty conforme o conteúdo.\n")
	b.WriteString("Mantenha EXATAMENTE " + fmt.Sprint(spec.Count) + " questões, cada uma com EXATAMENTE 5 alternativas.\n\n")
	b.WriteString("Tema: \"" + spec.Topic + "\"\n")
	if grounding := sourceList(sources); grounding != "" {
		b.WriteString("Fontes consultadas na pesquisa (confira fatos e gabaritos contra elas):\n" + grounding + "\n\n")
	}
	b.WriteString("Rascunho a revisar:\n" + string(qb) + "\n\n")
	b.WriteString("Responda SOMENTE com o conjunto final melhorado, em JSON válido no formato:\n" + questionsSchema)

	content, usage, err := c.CompleteUsage(ctx, cfg, []ai.Message{
		{Role: "system", Content: "Você é um revisor implacável de questões do ENEM e vestibulares brasileiros. Responda apenas com JSON válido."},
		{Role: "user", Content: b.String()},
	}, 0.3, true)
	if err != nil {
		return nil, usage, nil, err
	}
	parsed, err := parseQuestionsRecoveringElements(content)
	if err != nil {
		return nil, usage, nil, err
	}
	return parsed.Questions, usage, parsed.ElementIssues, nil
}

func parseQuestions(content string) ([]session.Question, error) {
	parsed, err := parseQuestionsPayload(content, false)
	if err != nil {
		return nil, err
	}
	return parsed.Questions, nil
}

func parseQuestionsRecoveringElements(content string) (parsedQuestions, error) {
	return parseQuestionsPayload(content, true)
}

func parseQuestionsPayload(content string, recoverElements bool) (parsedQuestions, error) {
	var q struct {
		Questions []session.Question `json:"questions"`
	}
	if err := json.Unmarshal([]byte(content), &q); err != nil {
		return parsedQuestions{}, fmt.Errorf("falha ao interpretar as questões: %v", err)
	}
	if len(q.Questions) == 0 {
		return parsedQuestions{}, fmt.Errorf("a IA não retornou questões válidas")
	}
	parsed := parsedQuestions{Questions: q.Questions}
	for i, question := range q.Questions {
		valid := make([]elements.Element, 0, len(question.Elements))
		for j, element := range question.Elements {
			if err := element.Validate(); err != nil {
				if !recoverElements {
					return parsedQuestions{}, fmt.Errorf("questão %d com elementos inválidos: elemento %d: %w", i+1, j+1, err)
				}
				parsed.ElementIssues = append(parsed.ElementIssues, fmt.Sprintf("questão %d, elemento %d: %v", i+1, j+1, err))
				continue
			}
			valid = append(valid, element)
		}
		if len(valid) > elements.MaxElements {
			if !recoverElements {
				return parsedQuestions{}, fmt.Errorf("questão %d com elementos inválidos: at most %d elements are allowed", i+1, elements.MaxElements)
			}
			parsed.ElementIssues = append(parsed.ElementIssues, fmt.Sprintf("questão %d: elementos excedentes removidos", i+1))
			valid = valid[:elements.MaxElements]
		}
		parsed.Questions[i].Elements = valid
	}
	return parsed, nil
}

// validate enforces the quiz contract: exact count, 5 unique non-empty
// options per question, correct index in range and non-empty texts.
func validate(qs []session.Question, count int) error {
	if len(qs) != count {
		return fmt.Errorf("esperava %d questões, recebi %d", count, len(qs))
	}
	for i, q := range qs {
		n := i + 1
		if strings.TrimSpace(q.Text) == "" {
			return fmt.Errorf("questão %d sem enunciado", n)
		}
		if len(q.Options) != 5 {
			return fmt.Errorf("questão %d tem %d alternativas, queria exatamente 5", n, len(q.Options))
		}
		seen := make(map[string]bool, len(q.Options))
		for _, opt := range q.Options {
			if strings.TrimSpace(opt) == "" {
				return fmt.Errorf("questão %d tem alternativa vazia", n)
			}
			if seen[opt] {
				return fmt.Errorf("questão %d tem alternativa duplicada", n)
			}
			seen[opt] = true
		}
		if q.Correct < 0 || q.Correct > 4 {
			return fmt.Errorf("questão %d com gabarito fora do intervalo 0-4: %d", n, q.Correct)
		}
		if strings.TrimSpace(q.Explanation) == "" {
			return fmt.Errorf("questão %d sem explicação", n)
		}
		if len([]rune(q.Skill)) > 120 {
			return fmt.Errorf("questão %d com habilidade longa demais", n)
		}
		difficulty := strings.ToLower(strings.TrimSpace(q.Difficulty))
		if difficulty != "" && difficulty != "easy" && difficulty != "medium" && difficulty != "hard" {
			return fmt.Errorf("questão %d com difficulty inválido: %q", n, q.Difficulty)
		}
		if err := elements.ValidateAll(q.Elements); err != nil {
			return fmt.Errorf("questão %d com elementos inválidos: %w", n, err)
		}
	}
	return nil
}
