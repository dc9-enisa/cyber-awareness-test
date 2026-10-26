# Aplikacja webowa testu z logowaniem Microsoft Entra ID

Wersja webowa aplikacji testowej kursu „Cyberbezpieczeństwo w codziennej pracy”. Uczestnik loguje się kontem służbowym, rozwiązuje diagnozę wstępną (11 pytań) i test końcowy (25 pytań, próg 20). Po zaliczeniu testu pobiera zaświadczenie uczestnictwa w PDF. Osoby z rolą administratora widzą raport wyników i eksport CSV.

**Pytania:** repozytorium zawiera wyłącznie **pytania przykładowe** (`data/questions.json`, 3 + 5 pytań oznaczonych „PRZYKŁAD”). Prawdziwy bank pytań kursu z kluczem odpowiedzi nie jest publiczny – dostarcza go autor kursu i podaje się go przy wdrożeniu (sekcja 6).

**Status:** sprawdzone testami automatycznymi z symulowanym dostawcą tożsamości i lokalnie w trybie testowym. **Nie sprawdzono** z rzeczywistą dzierżawą Entra ani w kontenerze na serwerze zamawiającego.

## 1. Rejestracja aplikacji w Microsoft Entra ID

Wykonuje administrator dzierżawy w [Microsoft Entra admin center](https://entra.microsoft.com).

1. **Identity → Applications → App registrations → New registration**
   - Name: `Cyberbezpieczeństwo – test`
   - Supported account types: **Accounts in this organizational directory only (Single tenant)**
   - Redirect URI: platforma **Web**, adres `https://<adres-aplikacji>/auth/callback`
2. **Authentication** → w sekcji Web dodaj drugi Redirect URI: `https://<adres-aplikacji>/wylogowano`. Entra przekierowuje po wylogowaniu tylko na zarejestrowane adresy. Pozostaw wyłączone „Access tokens” i „ID tokens” (implicit grant) – aplikacja używa przepływu kodu z PKCE.
3. **Certificates & secrets → Client secrets → New client secret**. Skopiuj **Value** do `ENTRA_CLIENT_SECRET`. Zapisz datę wygaśnięcia; przed nią utwórz nowy sekret i zrestartuj kontener.
4. **App roles → Create app role**
   - Display name: `Raport wyników`, Allowed member types: **Users/Groups**
   - Value: `Raporty` (musi być równe `ADMIN_ROLE`)
   - utwórz drugą rolę: Display name `Uczestnik`, Value `Uczestnik` – do przypisywania uczestników (gdy aplikacja ma role, Entra nie przypisze użytkownika z *Default Access*)
5. **Token configuration → Add optional claim → ID → email** (opcjonalnie; login bierze się z `preferred_username`).
6. **API permissions**: wystarczą domyślne uprawnienia delegowane `openid`, `profile`, `email` (Microsoft Graph). W razie potrzeby **Grant admin consent**.
7. **Enterprise applications → (ta aplikacja) → Properties → Assignment required? = Yes**. Następnie **Users and groups**: przypisz grupę uczestników (rola `Uczestnik`) oraz osoby lub grupę administratorów raportu (rola `Raport wyników`). Bez przypisania logowanie zostanie odrzucone przez Entra.
8. Z karty **Overview** skopiuj **Directory (tenant) ID** → `ENTRA_TENANT_ID` i **Application (client) ID** → `ENTRA_CLIENT_ID`.

## 2. Uruchomienie w kontenerze

Wymagania: Docker z Compose, serwer pośredniczący z certyfikatem TLS (nginx, Traefik, Caddy, Azure Application Gateway lub podobny) kierujący ruch z `https://<adres-aplikacji>` na `127.0.0.1:8080`.

```sh
cp .env.przyklad .env        # uzupełnij wartości (sekcja 3)
docker compose up -d --build
docker compose logs -f       # „wersja pytań …, nasłuch :8080 …”
```

Obraz buduje się w dwóch etapach: kompilacja z testami (`go vet`, `go test`), potem obraz `gcr.io/distroless/static-debian12:nonroot` – bez powłoki, użytkownik 65532, system plików tylko do odczytu, zapis wyłącznie w wolumenie `/data`. Program ma wbudowany `healthcheck` dla Dockera.

Bez Compose:

```sh
docker build -t cyberbezpieczenstwo-test:1.3 .
docker run -d --name test --env-file .env -p 127.0.0.1:8080:8080 \
  -v wyniki:/data --read-only --cap-drop ALL --security-opt no-new-privileges \
  cyberbezpieczenstwo-test:1.3
```

Aplikacja jest jednym statycznym plikiem bez zależności zewnętrznych (tylko biblioteka standardowa Go), więc można ją też uruchomić bez kontenera: `go build -o web-app .` i te same zmienne środowiskowe.

## 3. Zmienne środowiskowe

| Zmienna | Wymagana | Opis |
|---|---|---|
| `BASE_URL` | tak | Publiczny adres aplikacji, np. `https://szkolenie.firma.pl`. Musi być `https`, poza `http://localhost`. Na jego podstawie powstają adresy powrotu z Entra |
| `ENTRA_TENANT_ID` | tak | Directory (tenant) ID |
| `ENTRA_CLIENT_ID` | tak | Application (client) ID |
| `ENTRA_CLIENT_SECRET` | tak | Wartość sekretu klienta. Nie zapisuj w repozytorium |
| `SESSION_KEY` | tak | Klucz podpisu ciasteczek, min. 32 bajty: `openssl rand -base64 32`. Zmiana unieważnia wszystkie sesje |
| `ORG_NAME` | tak | Nazwa organizacji na zaświadczeniu i w informacji o danych |
| `PRIVACY_TEXT` | zalecana | Akapit „Administrator danych i kontakt” na stronie `/prywatnosc` |
| `ADMIN_ROLE` | nie | Wartość roli aplikacji z dostępem do raportu (domyślnie `Raporty`) |
| `ADMIN_UPNS` | nie | Loginy administratorów raportu bez przypisanej roli, po przecinku |
| `RETENTION_DAYS` | nie | Po ilu dniach wyniki są usuwane (domyślnie 365; `0` = bez usuwania) |
| `CERT_REQUIRE_PASS` | nie | `1` (domyślnie) – zaświadczenie po zaliczeniu testu końcowego; `0` – po każdym oddanym teście końcowym |
| `DATA_DIR` | nie | Katalog wyników (w obrazie `/data`) |
| `LISTEN` | nie | Adres nasłuchu (w obrazie `:8080`) |
| `CLIENT_IP_HEADER` | zalecana za proxy | Nagłówek z adresem klienta od serwera pośredniczącego, np. `CF-Connecting-IP` (Cloudflare) lub `X-Real-IP`. Używany do limitów żądań i dziennika. Ustaw tylko, gdy port aplikacji jest dostępny wyłącznie dla serwera pośredniczącego |
| `QUESTIONS_FILE` | nie | Ścieżka do pliku banku pytań (JSON) zamiast wbudowanych pytań przykładowych, np. `/pytania/questions.json` |
| `DEV_LOGIN` | nie | `1` – logowanie testowe bez Entra. Dozwolone tylko przy `BASE_URL` na localhost; nie do produkcji |

## 4. Działanie

- **Logowanie:** OpenID Connect, przepływ kodu autoryzacji z PKCE (S256), klient poufny. Aplikacja sprawdza podpis RS256 tokenu kluczami z JWKS Entra (z obsługą rotacji kluczy), wystawcę, odbiorcę, dzierżawę (`tid`), ważność, `nonce` oraz `state`. Sesja to podpisane HMAC-SHA256 ciasteczko `HttpOnly`, `Secure`, `SameSite=Lax`, ważne 8 godzin. Wylogowanie kończy też sesję w Entra.
- **Zabezpieczenia (OWASP Top 10):**
  - A01: uczestnik widzi tylko swoje próby i zaświadczenia, raport tylko z rolą; wylogowanie unieważnia sesję (ponowne użycie skopiowanego ciasteczka nie działa),
  - A02: ciasteczka `__Host-`, `Secure`, `HttpOnly`, `SameSite=Lax`, podpis HMAC-SHA256; HSTS 2 lata,
  - A03: szablony z automatycznym kodowaniem, ochrona przed formułami w CSV, CSP `default-src 'none'` bez `unsafe-inline`,
  - A04: limity żądań (logowanie 20/min, ogółem 600/min na adres IP), limit treści formularza 64 KB, nagłówków 16 KB, limity czasu,
  - A05: nagłówki `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`, `Permissions-Policy`, `Cross-Origin-Opener/Resource/Embedder-Policy`, `X-Permitted-Cross-Domain-Policies`, `Cache-Control: no-store`; brak listingu katalogów, tylko zadeklarowane metody HTTP,
  - A06: tylko biblioteka standardowa Go, obraz distroless; aktualizuj obraz bazowy i sprawdzaj `govulncheck ./...`,
  - A07: OIDC z PKCE, `nonce`, `state`, weryfikacja podpisu i oświadczeń; limit prób logowania,
  - A08: bank pytań walidowany przy starcie, obraz budowany z testami,
  - A09: dziennik zdarzeń bezpieczeństwa w JSON (`"kanal":"bezpieczenstwo"`): logowania udane i nieudane, wylogowania, odmowy dostępu, raport, eksport CSV, pobranie cudzego zaświadczenia przez administratora, błędy CSRF, przekroczenia limitów – bez tokenów i treści odpowiedzi,
  - A10: połączenia wychodzące tylko do hosta `ENTRA_AUTHORITY`; adresy z metadanych OIDC spoza tego hosta są odrzucane.
- **Test:** pytanie po pytaniu, powrót i zmiana odpowiedzi do oddania, omówienie po oddaniu – jak w aplikacji offline. Próba w toku jest tylko w pamięci serwera (wygasa po 12 godzinach lub po restarcie). Próbę widzi tylko jej właściciel.
- **Zaświadczenie uczestnictwa:** PDF A4 poziomo, 2 strony. Strona 1: imię i nazwisko z Entra, data, wynik, numer (`XXXX-XXXX-XXXX-XXXX`, 80 losowych bitów) i adres weryfikacji. Strona 2: zakres szkolenia – moduły i zagadnienia. Na obu stronach w stopce znak ENISA (na białym polu, z łączem do strony agencji) i znak „Finansowane przez Unię Europejską” (nie mniejszy niż znak ENISA). Wydawcą jest organizacja z `ORG_NAME`; zaświadczenie stwierdza, że nie zostało wydane ani poświadczone przez ENISA ani UE – zasady ENISA nie pozwalają, by logo sugerowało certyfikację lub poparcie.
- **Znaki ENISA i UE** są w stopce każdej strony aplikacji (pochodzenie: `assets/ZRODLO_ZNAKOW.md`).
- **Weryfikacja:** `/weryfikacja/<numer>` – po zalogowaniu dowolny pracownik dzierżawy może sprawdzić, czy zaświadczenie istnieje (imię i nazwisko, data, wynik).
- **Raport (`/admin`):** liczba uczestników, ukończone części, odsetek zaliczeń, odsetek poprawnych odpowiedzi w każdym module (diagnoza i test końcowy), liczba prób z błędem w pytaniu KLUCZOWYM, lista uczestników z najlepszym wynikiem i zaświadczeniem, wyszukiwanie. **Eksport CSV** (UTF-8 z BOM, separator „;”, otwiera się w Excelu z polskimi ustawieniami) zawiera wszystkie oddane próby z wynikami modułów.

## 5. Dane osobowe (RODO)

Zapisywane w `/data/wyniki.jsonl` dla każdej oddanej próby: identyfikator konta Entra (`oid`), imię i nazwisko, login, część testu, data, liczba punktów ogółem i w modułach, zaliczenie, moduły z błędem w pytaniu KLUCZOWYM, numer zaświadczenia. **Treść odpowiedzi nie jest zapisywana.** Aplikacja nie używa narzędzi analitycznych ani zewnętrznych zasobów.

Przed uruchomieniem organizacja (administrator danych) powinna:

- określić podstawę przetwarzania i uzupełnić `PRIVACY_TEXT` (administrator, kontakt, IOD),
- ustalić okres przechowywania (`RETENTION_DAYS`) i osoby z rolą `Raport wyników`,
- objąć wolumen `/data` kopią zapasową i ograniczyć do niego dostęp,
- ocenić, czy potrzebna jest ocena skutków (DPIA) – zależy od skali i sposobu wykorzystania wyników.

Usunięcie danych jednej osoby: zatrzymaj kontener, usuń z `wyniki.jsonl` wiersze z jej `oid` (każdy wiersz to jeden obiekt JSON), uruchom ponownie.

## 6. Bank pytań

Format `data/questions.json`: `version`, `funding`, `threshold` (próg zaliczenia testu końcowego), `modules` (kod, tytuł, zakres slajdów) i `parts` – część `A` (diagnoza) i `B` (test końcowy). Każde pytanie ma 4 odpowiedzi, literę klucza `A`–`D`, 4 uzasadnienia, moduł, znacznik `critical` (pytanie KLUCZOWE) i slajd do powtórki. Program sprawdza format przy starcie.

Pełny bank pytań kursu (diagnoza 11, test końcowy 25, próg 20) dostarcza autor kursu jako plik `questions.json`. Dwa sposoby użycia:

- **plik zamontowany w kontenerze (zalecane):** w `docker-compose.yml` dodaj wolumen `./pytania/questions.json:/pytania/questions.json:ro` i ustaw `QUESTIONS_FILE=/pytania/questions.json`. Plik nie trafia do obrazu ani do repozytorium,
- **wbudowany w obraz:** podmień `data/questions.json` przed `docker compose build`. Nie wysyłaj takiej zmiany do publicznego repozytorium.

Zmiana pytań nie wpływa na zapisane wyniki ani wydane zaświadczenia.

## 7. Sprawdzenie

```sh
go vet ./... && go test ./...
```

Testy nie zakładają liczby pytań – przechodzą z pytaniami przykładowymi i z pełnym bankiem (`QUESTIONS_FILE` nie wpływa na testy; aby sprawdzić pełny bank, podmień lokalnie `data/questions.json`). Obejmują pełną ścieżkę z symulowanym dostawcą tożsamości (discovery, JWKS, wymiana kodu z PKCE), odrzucenie tokenów z inną dzierżawą, innym odbiorcą lub wystawcą, wygasłych, z błędnym `nonce`, bez `oid`, z `alg=none`, z podmienioną treścią i nieznanym kluczem, podmieniony `state`, CSRF i obce `Origin`, izolację prób i zaświadczeń między użytkownikami, dostęp do raportu tylko z rolą, jednokrotny zapis wyniku, retencję, zapis po restarcie, treść zaświadczenia PDF (z `pdftotext`, jeśli jest zainstalowany) i format CSV.

Pokaz lokalny bez Entra:

```sh
BASE_URL=http://localhost:8080 DEV_LOGIN=1 SESSION_KEY=$(openssl rand -base64 32) \
ORG_NAME="Przykładowa Organizacja" DATA_DIR=./dane go run .
```

## 8. Ograniczenia

- Jedna instancja: wyniki w pliku, próby w toku w pamięci. Przy wielu replikach potrzebna byłaby baza danych i wspólny magazyn prób.
- Sesji nie da się unieważnić przed upływem 8 godzin inaczej niż zmianą `SESSION_KEY` (wylogowuje wszystkich). Odebranie dostępu w Entra działa przy następnym logowaniu.
- Raport nie ma podziału na działy – Entra przekazuje w tokenie tylko dane podstawowe.
