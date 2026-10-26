# Cyberbezpieczeństwo w codziennej pracy – test z logowaniem Microsoft Entra ID

Aplikacja webowa do diagnozy wstępnej i testu końcowego kursu e-learningowego „Cyberbezpieczeństwo w codziennej pracy”. Uczestnicy logują się kontem służbowym Microsoft Entra ID, rozwiązują test pytanie po pytaniu i po zaliczeniu pobierają zaświadczenie uczestnictwa w PDF. Administratorzy widzą raport wyników i eksport CSV.

- logowanie OpenID Connect (przepływ kodu z PKCE), rola aplikacji Entra dla raportu,
- wyniki bez treści odpowiedzi, automatyczne usuwanie po ustalonym czasie,
- zaświadczenie PDF z numerem do weryfikacji,
- jeden plik wykonywalny w Go bez zależności zewnętrznych, obraz Docker bez powłoki.

> Repozytorium zawiera tylko **pytania przykładowe**. Pełny bank pytań kursu nie jest publiczny i jest podawany przy wdrożeniu (`QUESTIONS_FILE`).

## Szybki start (pokaz lokalny, bez Entra)

```sh
BASE_URL=http://localhost:8080 DEV_LOGIN=1 SESSION_KEY=$(openssl rand -base64 32) \
ORG_NAME="Przykładowa Organizacja" DATA_DIR=./dane go run .
```

Otwórz http://localhost:8080. Tryb `DEV_LOGIN` działa tylko na localhost i nie nadaje się do produkcji.

## Wdrożenie

Rejestracja aplikacji w Entra, konfiguracja, Docker, dane osobowe i ograniczenia: [WDROZENIE.md](WDROZENIE.md).

```sh
cp .env.przyklad .env   # uzupełnij
docker compose up -d --build
```

## Testy

```sh
go vet ./... && go test ./...
```

Testy symulują dostawcę tożsamości Entra (discovery, JWKS, wymiana kodu) i sprawdzają m.in. odrzucanie nieprawidłowych tokenów, CSRF, izolację danych użytkowników, raport, CSV i zaświadczenie.

## Znaki i kroje

- `assets/enisa_sygnet.png`, `web/static/enisa_sygnet.png` – znak ENISA; `assets/eu_finansowane.png`, `web/static/eu_finansowane.png` – znak „Finansowane przez Unię Europejską” (Komisja Europejska). Bez zmian poza skalą; pochodzenie i zasady użycia: [assets/ZRODLO_ZNAKOW.md](assets/ZRODLO_ZNAKOW.md).
- Schibsted Grotesk – SIL Open Font License 1.1 (`assets/OFL.txt`).

Znak ENISA nie oznacza certyfikacji ani poparcia przez ENISA. Zaświadczenie wydaje organizacja wdrażająca aplikację; nie jest certyfikatem ENISA ani Unii Europejskiej.
