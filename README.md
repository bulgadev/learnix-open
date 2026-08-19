# Learnix

Tutor guiado por IA com material de estudo sob demanda e bateria de questões
estilo ENEM que se adapta aos seus erros. Cada estudo é um espaço de trabalho:
arquivos e notas em Markdown (com versões e ramificações), várias conversas com
o tutor (ramificáveis), e pesquisa na web com citações quando uma chave Tavily é
configurada. Construído como um proof-of-concept real: autenticação local
(email + senha), persistência em SQLite, e provas salvas por usuário que podem
ser revisitadas depois.

A instância roda com uma única chave de API do dono: cada novo usuário recebe
250.000 tokens, gerenciados num painel de administração — defina o admin com
`ADMIN_EMAIL` (veja [`docs/run.md`](docs/run.md)).

## Começar

Veja [`docs/run.md`](docs/run.md) para instruções completas.

Resumo:

```bash
API_KEY=sk-... ./run.sh
```

## Documentação

- [`docs/INDEX.md`](docs/INDEX.md) — índice da documentação
- [`docs/architecture.md`](docs/architecture.md) — arquitetura e fluxo de request
- [`docs/run.md`](docs/run.md) — como rodar
- [`docs/api.md`](docs/api.md) — rotas HTTP
- [`docs/auth.md`](docs/auth.md) — autenticação
- [`docs/persistence.md`](docs/persistence.md) — persistência e schema
- [`docs/testing.md`](docs/testing.md) — como rodar e escrever testes

## Stack

Go 1.26 · chi v5 · templ · SQLite (modernc.io/sqlite) · bcrypt · Tavily (web search) · Tailwind/htmx/Alpine/marked/DOMPurify self-hosted (`static/vendor`)
