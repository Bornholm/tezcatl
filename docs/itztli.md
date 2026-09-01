# Itztli, l'interface web

Itztli est l'interface web de consultation d'un serveur tezcatl. C'est
un binaire séparé et optionnel : le serveur fonctionne à l'identique
sans lui, et il peut tourner sur une autre machine que le serveur.

Trois écrans, volontairement peu denses :

- **Incidents** : l'écran d'accueil liste les derniers incidents,
  assemblés à la volée depuis le journal d'événements du serveur avec
  le même regroupement que `tezcatl incidents`. Trois filtres en tête
  de page : la période, la sévérité minimale et le gap de regroupement.
  Le détail d'un incident montre le déclencheur, les changements
  corrélés (étiquetés comme une corrélation, rien d'autre), les preuves
  agrégées, les lignes de log autour du déclencheur et les événements
  sous-jacents, chacun avec un raccourci de marquage.
- **Templates** : les templates appris, filtrables par texte et par
  marquage. Les boutons `ignorer`, `normal`, `symptomatique` et
  `effacer` appellent la même API que `tezcatl mark` et prennent effet
  tout de suite.
- **Métriques** : les séries apprises, triées par écart à leur
  baseline, avec le marquage `ignorer` par série. Pas d'historique de
  valeurs : le serveur ne conserve que moyenne, écart type et niveau
  récent, et l'interface n'affiche que ce qui existe.

Itztli ne stocke rien : chaque page est une lecture de l'API
d'administration du serveur. Fermer l'onglet ne perd rien, redémarrer
itztli non plus.

## Filtrer la liste

À l'ouverture, la liste montre **les incidents critiques des dernières
24 heures** : ce qui demande une réponse maintenant, pas deux semaines
d'histoire. Les valeurs de départ se règlent par
`incidents.default_range` et `incidents.default_severity`.

Trois réglages, chacun une rangée de puces :

- **période** : 1 h, 6 h, 24 h, 7 j, ou toute la fenêtre chargée.
- **sévérité** : c'est un plancher, « warning et + » montre aussi les
  critiques.
- **regroupement** : le silence après lequel un incident est clos. Un
  gap court découpe une nuit agitée en incidents distincts, un gap long
  les fond en une seule histoire. Le changer ne recharge rien depuis le
  serveur : les événements de la fenêtre sont déjà là, seul le
  regroupement est refait.

Le filtre voyage dans l'URL, une vue se partage donc telle qu'elle est
lue. Le lien vers un incident emporte le gap, sans quoi la page de
détail regrouperait autrement que la liste dont elle vient.

## Marquer depuis un incident

Le détail d'un incident porte, sous le déclencheur et sous chaque
ligne de preuve, un raccourci `symptomatique` / `ignorer` / `défaut`.
C'est la boucle de `tezcatl mark`, au moment où le jugement se fait :
le lecteur vient de voir pourquoi ce motif compte.

Le raccourci vise le template du signal le plus fort de la ligne, ou sa
série pour un signal de métrique — une série ne connaît que `ignorer`,
`symptomatique` n'y a pas de sens. Un template marqué `normal` depuis
la page Templates s'affiche comme silencié : les détecteurs traitent
`normal` et `ignore` de la même façon.

## Installation

Par le script d'installation, en ajoutant `--itztli` à n'importe
quelle variante :

```bash
curl -fsSL https://raw.githubusercontent.com/bornholm/tezcatl/main/install.sh | sh -s -- --variant server --itztli
```

Le paquet `tezcatl-itztli` installe le binaire, l'unité systemd
`tezcatl-itztli`, la configuration `/etc/tezcatl/itztli.yaml` et un
utilisateur système dédié. Il reste deux gestes :

```bash
echo 'ITZTLI_PASSWORD=votre-mot-de-passe' >> /etc/tezcatl/itztli.env
systemctl enable --now tezcatl-itztli
```

L'interface écoute alors sur `http://127.0.0.1:8484`. Itztli ne
termine pas le TLS : pour l'exposer, mettez un reverse proxy devant et
renseignez `server.base_url` avec l'URL publique, les cookies de
session passent en `Secure` dès qu'elle est en `https`.

Hors paquets, l'archive `itztli_<version>_<os>_<arch>.tar.gz` de
chaque release contient le binaire et le profil de configuration
commenté.

## Configuration

Le profil commenté complet est dans
[misc/config/itztli.yaml](../misc/config/itztli.yaml) (installé sous
`/usr/share/doc/tezcatl/itztli.yaml` par le paquet). Le minimum :

```yaml
server:
  listen: 127.0.0.1:8484

tezcatl:
  target: tcp://127.0.0.1:4242

auth:
  mode: password
  password:
    password: ${ITZTLI_PASSWORD}
```

### Authentification

Deux modes, et un seul à la fois ; la page de connexion ne propose
jamais d'alternative.

`password` est un compte local unique, sans identifiant. Le mot de
passe vient d'une variable d'environnement, ou d'une empreinte bcrypt
qui évite de stocker le secret même dans l'environnement :

```yaml
auth:
  mode: password
  password:
    # htpasswd -bnBC 10 "" 'secret' | tr -d ':\n'
    password_hash: $2y$10$...
```

`oidc` délègue à un fournisseur d'identité (Keycloak, Authentik…) par
le flux authorization code. `server.base_url` devient obligatoire,
l'URL de retour à déclarer chez le fournisseur est
`<base_url>/oidc/callback` :

```yaml
server:
  base_url: https://itztli.example.com

auth:
  mode: oidc
  oidc:
    issuer: https://keycloak.example.com/realms/main
    client_id: itztli
    client_secret: ${ITZTLI_OIDC_SECRET}
    button_label: Se connecter avec Keycloak
```

Quiconque passe l'authentification voit tout et peut tout marquer : il
n'y a pas de rôles. Ne donnez l'accès qu'à des opérateurs.

### Le bouton Explain

Avec une section `genai`, chaque incident gagne un bouton qui envoie
son rapport auto-descriptif (le même que
`tezcatl incidents --format markdown`) à un LLM et affiche sa lecture :

```yaml
genai:
  provider: mistral        # openai, mistral ou openrouter
  model: mistral-large-latest
  api_key: ${ITZTLI_GENAI_API_KEY}
```

`base_url` pointe vers n'importe quel endpoint compatible OpenAI, y
compris un modèle local. Sans section `genai`, le bouton n'existe pas.

La génération tourne côté serveur, hors de la requête qui l'a lancée,
et la page interroge le résultat toutes les deux secondes. Un modèle
qui met trois minutes aboutit donc quand même derrière un reverse
proxy, dont le délai d'attente vaut souvent soixante secondes : rien à
régler de ce côté. Fermer l'onglet n'annule pas un appel déjà payé, et
y revenir retrouve la réponse. Elle reste affichée avec son
avertissement, conservée une demi-heure en mémoire, puis oubliée :
c'est une lecture du rapport, pas une donnée.

## Ce qu'itztli ne fait pas

- Pas de graphiques d'historique : le serveur ne conserve pas les
  valeurs, seulement les baselines. Une interface qui tracerait des
  courbes inventerait des données.
- Pas d'identité d'incident durable : les incidents sont assemblés à
  chaque lecture depuis la fenêtre configurée (`incidents.window`,
  bornée par la rétention du serveur). Un lien vers un incident sorti
  de la fenêtre ramène à l'accueil.
- Pas d'écriture en dehors du marquage : templates et métriques,
  exactement ce que permet l'API d'administration.
