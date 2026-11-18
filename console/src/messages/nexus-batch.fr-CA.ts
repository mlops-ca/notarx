import type { nexusBatchEnCA } from "./nexus-batch.en-CA";

export const nexusBatchFrCA: Record<keyof typeof nexusBatchEnCA, string> = {
  "nexusBatch.sourceView": "Connexions et mappages",
  "nexusBatch.batchView": "Lots et acceptation",
  "nexusBatch.title": "Lots de migration Nexus",
  "nexusBatch.background": "Les lots se poursuivent en arrière-plan",
  "nexusBatch.backgroundHint":
    "Fermer cette page ou arrêter l’interrogation n’annule pas le travail. La pause empêche le démarrage de nouvelles entrées; une entrée en cours peut se terminer. L’annulation conserve le contenu cible publié.",
  "nexusBatch.new": "Créer un lot de migration",
  "nexusBatch.useMappings": "Créer un lot avec {count} mappages hébergés",
  "nexusBatch.rescan": "Réanalyser et rattraper",
  "nexusBatch.rescanHint":
    "Crée un nouveau lot et énumère entièrement la source. Les identités identiques sont revérifiées, les conflits sont conservés et les suppressions à la source sont seulement signalées.",
  "nexusBatch.start": "Appliquer les mappages et créer le lot",
  "nexusBatch.close": "Fermer",
  "nexusBatch.empty": "Aucun lot de migration pour l’instant",
  "nexusBatch.emptyHint":
    "Enregistrez et inspectez une source, liez de vrais dépôts hébergés cibles, puis créez un lot à l’aide de preuves vérifiées de capacité de stockage.",
  "nexusBatch.select":
    "Choisir un lot pour inspecter la progression et les rapports",
  "nexusBatch.refresh": "Actualiser les lots",
  "nexusBatch.more": "Charger plus de lots",
  "nexusBatch.readLatest": "Lire le dernier lot",
  "nexusBatch.source": "Source de migration enregistrée",
  "nexusBatch.target": "Dépôt hébergé cible existant",
  "nexusBatch.targetFor": "Dépôt cible pour {name}",
  "nexusBatch.targetHint":
    "Liez de vrais dépôts hébergés actifs du même format. Créez d’abord explicitement les cibles manquantes dans la gestion des dépôts; les politiques et les permissions de Nexus ne sont pas héritées.",
  "nexusBatch.manageRepositories": "Ouvrir la gestion des dépôts",
  "nexusBatch.moreRepositories": "Charger plus de dépôts cibles",
  "nexusBatch.moreSources": "Charger plus de sources",
  "nexusBatch.mappingSummary": "{source} → {target}",
  "nexusBatch.explicitMapping":
    "Les mappages deviennent des entrées immuables du lot. Le contenu hébergé sélectionné des versions release Maven, ainsi que Raw, OCI, Conan 2 et npm, est copié. Les paquets npm sont validés comme des ensembles de versions complets; des tags ou des métadonnées de version existants différents sont en conflit. Configurez les proxys et les groupes séparément.",
  "nexusBatch.capabilities": "Capacité d’exécution et preuves de capacité",
  "nexusBatch.disabled":
    "Les écritures de migration sont désactivées dans ce déploiement",
  "nexusBatch.disabledHint":
    "Configurez un vrai pool de stockage d’objets et activez un worker de migration. Les connexions, les observations d’inventaire et les rapports existants restent lisibles; une capacité inconnue empêche le démarrage du travail.",
  "nexusBatch.binding": "Cible {target} · Pool de stockage {pool}",
  "nexusBatch.capacityHint":
    "Saisissez les preuves externes vérifiées pour ce vrai pool de stockage. Chaque champ d’octets doit être connu (saisissez 0 seulement pour un zéro explicite). Le pic de téléchargement doit couvrir au moins le plus grand spool complet d’une ressource source. Ce n’est ni une mesure de la passerelle ni une réservation. Ne substituez pas la capacité des journaux ou des montages temporaires.",
  "nexusBatch.observedAt":
    "Heure d’observation de la capacité (avec fuseau horaire)",
  "nexusBatch.validUntil":
    "Heure d’expiration de la preuve (avec fuseau horaire)",
  "nexusBatch.timeHint":
    "Utilisez des horodatages ISO avec fuseau horaire. Une observation ne peut pas être dans le futur et la validité est d’au plus une heure. La reprise ou la nouvelle tentative exige une observation strictement plus récente.",
  "nexusBatch.freeBytes": "Octets libres du pool de stockage",
  "nexusBatch.downloadBytes": "Octets de pic de téléchargement",
  "nexusBatch.uploadBytes": "Octets de pic de téléversement",
  "nexusBatch.backupBytes": "Octets de pic de sauvegarde",
  "nexusBatch.restoreBytes": "Octets de pic de restauration",
  "nexusBatch.headroomBytes": "Octets de marge",
  "nexusBatch.required":
    "Saisissez une valeur vérifiée. Les valeurs inconnues empêchent de démarrer ou de reprendre le travail.",
  "nexusBatch.invalidNumber":
    "Saisissez un nombre entier non négatif d’octets, dans la plage d’entiers exacts du navigateur.",
  "nexusBatch.invalidTime":
    "Saisissez un horodatage ISO valide incluant un fuseau horaire.",
  "nexusBatch.expired":
    "La preuve a expiré, provient du futur ou dépasse la fenêtre de validité d’une heure. Vérifiez une nouvelle observation.",
  "nexusBatch.newerEvidence":
    "La reprise ou la nouvelle tentative exige une observation strictement plus récente que la preuve précédente.",
  "nexusBatch.previousEvidence":
    "Observation précédente\u00a0: {time}. Les anciennes preuves ne sont pas préremplies.",
  "nexusBatch.pause": "Mettre le lot en pause",
  "nexusBatch.resume": "Reprendre le lot",
  "nexusBatch.retry": "Réessayer les entrées en échec admissibles",
  "nexusBatch.retryHint":
    "Une analyse non scellée relance l’analyse complète dans le même lot. Une fois scellée, seules les entrées en échec ou dont la source est manquante, sous la limite de {attempts} tentatives, sont remises en file. Les conflits et les entrées non prises en charge ne sont jamais écrasés.",
  "nexusBatch.cancel": "Annuler le lot",
  "nexusBatch.cancelTitle": "Annuler ce lot de migration?",
  "nexusBatch.cancelHint":
    "Arrête le nouveau travail et bloque les publications suivantes. Les identités validées et le contenu source sont conservés. Cela ne supprime ni n’annule aucune donnée.",
  "nexusBatch.scanning": "Analyse et vérification de la source",
  "nexusBatch.ready": "En attente d’un worker d’arrière-plan",
  "nexusBatch.running": "Migration en cours",
  "nexusBatch.pausing": "Vidage de l’entrée en cours",
  "nexusBatch.paused": "En pause",
  "nexusBatch.blocked": "Bloqué; action requise",
  "nexusBatch.completed": "Lot d’arrière-plan terminé",
  "nexusBatch.failed": "Échec du lot d’arrière-plan",
  "nexusBatch.cancelled": "Annulé",
  "nexusBatch.version": "Version du lot {version}",
  "nexusBatch.updated": "Dernière mise à jour du serveur\u00a0: {time}",
  "nexusBatch.scanned": "Enregistrements de métadonnées source découverts",
  "nexusBatch.planned": "Entrées planifiées scellées",
  "nexusBatch.total": "Octets source principaux pris en charge, uniques",
  "nexusBatch.unknown": "Inconnu; le manifest complet n’est pas scellé",
  "nexusBatch.sourceVerification":
    "Octets de vérification de la source lus pendant l’analyse",
  "nexusBatch.sourceVerificationHint":
    "Lectures supplémentaires de la source pour compléter les hachages complets manquants du manifest. Consignées même pour les analyses incomplètes ou en échec; ce ne sont pas des octets copiés.",
  "nexusBatch.transferred": "Octets source réellement lus à l’étape de copie",
  "nexusBatch.transferredHint":
    "Inclut les nouvelles tentatives et la revalidation à la source des identités existantes; peut dépasser le total du contenu unique.",
  "nexusBatch.verified":
    "Octets cibles uniques avec relecture complète vérifiée",
  "nexusBatch.visible": "Identités cibles visibles",
  "nexusBatch.pending": "En attente",
  "nexusBatch.uploaded": "Téléversé; publication incomplète",
  "nexusBatch.entryVerified": "Cible vérifiée; publication incomplète",
  "nexusBatch.committed": "Publié",
  "nexusBatch.skipped": "Ignoré après revalidation complète",
  "nexusBatch.conflict": "Conflit de cible; conservé",
  "nexusBatch.entryFailed": "Échec",
  "nexusBatch.unsupported": "Non pris en charge",
  "nexusBatch.sourceMissing": "Source manquante pendant la lecture",
  "nexusBatch.sourceDeleted":
    "Suppression à la source signalée par une analyse ultérieure",
  "nexusBatch.partial":
    "Des conflits, des échecs, du contenu non pris en charge ou des changements à la source subsistent. Cela ne prouve pas un rattrapage complet.",
  "nexusBatch.entries": "Entrées du lot et hachages complets",
  "nexusBatch.entry": "Entrée",
  "nexusBatch.state": "État du serveur",
  "nexusBatch.attempts": "Tentative {count} / {max}",
  "nexusBatch.asset": "Ressource",
  "nexusBatch.noEntries": "Aucune entrée exécutable pour l’instant",
  "nexusBatch.noEntriesHint":
    "Une liste d’entrées vide avant l’analyse complète et le scellement du manifest ne prouve pas que la source est vide.",
  "nexusBatch.loaded": "{loaded} / {total} entrées planifiées chargées",
  "nexusBatch.moreEntries": "Charger la page d’entrées suivante",
  "nexusBatch.allEntries": "Lire l’inventaire complet",
  "nexusBatch.sourcePath": "Chemin source",
  "nexusBatch.targetPath": "Chemin cible",
  "nexusBatch.sha256": "SHA-256 complet",
  "nexusBatch.assetVerified": "Cible entièrement relue et vérifiée",
  "nexusBatch.assetUploaded": "Octets cibles téléversés",
  "nexusBatch.assetUnverified": "Les octets cibles ne sont pas vérifiés",
  "nexusBatch.report": "Rapport d’observation d’acceptation de la migration",
  "nexusBatch.export": "Exporter le rapport d’observation actuel",
  "nexusBatch.reportHint":
    "Exporte les faits du lot côté serveur, l’inventaire chargé et les points d’accès cibles actuels, sans justificatifs de connexion à la source. Les preuves d’acceptation manquantes restent non vérifiées.",
  "nexusBatch.manifest": "SHA-256 du manifest scellé",
  "nexusBatch.inventoryIncomplete":
    "L’inventaire n’est pas entièrement chargé ou l’analyse n’est pas scellée. Ce rapport ne vaut pas acceptation de l’inventaire complet.",
  "nexusBatch.coldClient":
    "Acceptation à froid par un client, sur la cible seulement, pour ce lot\u00a0: non vérifiée",
  "nexusBatch.catchup":
    "Rattrapage final après gel des écritures à la source\u00a0: non vérifié",
  "nexusBatch.cutover": "Bascule et limites de données du retour arrière",
  "nexusBatch.cutoverHint":
    "Gelez explicitement les écritures à la source, effectuez une dernière réanalyse complète et vérifiez les clients à froid. Après de nouvelles écritures sur la cible, réconciliez-les et recopiez-les vers la source avant de revenir en arrière. Changer seulement les points d’accès fait perdre ces nouvelles écritures.",
  "nexusBatch.residual":
    "Dépendances Nexus résiduelles et changements côté consommateurs",
  "nexusBatch.residualHint":
    "Vérifiez les amonts et les membres des proxys et des groupes, les formats non pris en charge et les SNAPSHOT, les permissions et les justificatifs, les paramètres Maven, Gradle et Raw de l’IC, ainsi que les éditeurs de la source. Faites passer les consommateurs aux points d’accès cibles natifs. Cette page ne modifie ni l’IC, ni le DNS, ni l’Ingress, et ne prouve pas que Nexus peut être retiré.",
  "nexusBatch.endpoints":
    "Points d’accès cibles natifs à l’origine actuelle de la Console",
  "nexusBatch.endpointsHint":
    "Dérivés des vrais dépôts cibles. Confirmez les adresses publiques et l’authentification avant de modifier les consommateurs. Un point d’accès joignable n’est pas une preuve d’acceptation à froid par un client pour ce lot.",
  "nexusBatch.targetUnavailable":
    "Les détails de la cible sont indisponibles; son point d’accès et sa configuration ne sont pas confirmés.",
  "nexusBatch.requestFailed":
    "La requête du lot ne s’est pas terminée. Lisez l’état actuel du serveur avant de réessayer. Aucun justificatif de la source ni corps de réponse de l’amont n’est affiché.",
  "nexusBatch.unknownOutcome":
    "Le résultat de l’envoi n’est pas confirmé. Actualisez d’abord les lots. Réessayer avec les mêmes entrées réutilise la même requête pour éviter une création ou une commande en double.",
  "nexusBatch.versionConflict":
    "Un worker ou un autre administrateur a mis à jour le lot. Une lecture du serveur a été tentée; sélectionnez de nouveau l’opération et vérifiez la nouvelle version. Les écritures ne sont jamais renvoyées automatiquement.",
  "nexusBatch.idempotencyConflict":
    "Cet identifiant de requête a été utilisé avec un contenu différent. Lisez les faits du serveur avant de lancer une autre opération.",
  "nexusBatch.capacityUnknown":
    "La capacité est inconnue et les écritures sur la cible sont bloquées. Vérifiez chaque champ de capacité.",
  "nexusBatch.capacityInsufficient":
    "La capacité du pool cible ou le quota du dépôt est insuffisant. Rétablissez la capacité ou vérifiez de nouvelles preuves avant de reprendre.",
  "nexusBatch.capacityBinding":
    "La preuve ne correspond pas à la vraie cible ou au vrai pool de stockage. Relisez la capacité et vérifiez sa liaison.",
  "nexusBatch.sourceChanged":
    "La version de la connexion source a changé. Inspectez-la de nouveau et créez explicitement un nouveau lot; ce lot ne peut pas continuer.",
  "nexusBatch.invalidState":
    "L’état actuel du serveur ne permet pas cette action. Lisez le dernier lot.",
  "nexusBatch.sourceFailure":
    "Les lectures, les hachages ou l’appartenance à la source ont changé. Inspectez la source avant de continuer; les conflits de cibles ont été conservés.",
  "nexusBatch.scanFailure": "Dernier échec d’analyse persistant",
  "nexusBatch.scanStageLabel": "Étape de l’échec",
  "nexusBatch.scanCode": "Code de diagnostic borné",
  "nexusBatch.scanScope": "Portée d’analyse des dépôts sélectionnés",
  "nexusBatch.scanOccurred": "Échec observé à",
  "nexusBatch.scanMetadata": "Découverte des métadonnées de la source",
  "nexusBatch.scanManifest": "Planification du manifest",
  "nexusBatch.scanOpen": "Ouverture du flux de vérification de la source",
  "nexusBatch.scanRead": "Lecture du flux de vérification de la source",
  "nexusBatch.scanDigest": "Vérification du digest de la ressource source",
  "nexusBatch.scanVersion": "Revalidation de la version de la connexion source",
  "nexusBatch.scanNPM":
    "Vérification du packument npm et de l’ensemble complet de versions",
  "nexusBatch.scanPyPISimple": "Vérification de l’index des paquets Python",
  "nexusBatch.scanPyPIDistribution":
    "Vérification de l’identité de la distribution Python",
  "nexusBatch.pypiClosure":
    "Les fichiers de la version PyPI ou les métadonnées de l’index sont incomplets ou incohérents. Vérifiez la version à la source et réanalysez.",
  "nexusBatch.pypiIdentity":
    "Les métadonnées de l’archive ne correspondent pas au projet et à la version PyPI. Vérifiez la distribution source et réanalysez.",
  "nexusBatch.pypiAdministrator":
    "La migration PyPI exige un compte administrateur actif ou une clé d’API d’administrateur. Vérifiez l’accès actuel du propriétaire du lot ou de la planification.",
  "nexusBatch.pypiAuthorityOrTarget":
    "Le propriétaire ou la cible de la migration PyPI est indisponible. Vérifiez l’accès actuel du propriétaire et que la cible est un dépôt hébergé actif.",
  "nexusBatch.npmClosure":
    "Les métadonnées npm et l’ensemble de versions sont incomplets ou incohérents. Vérifiez l’accès en lecture au dépôt sélectionné et à chaque version, puis réanalysez.",
  "nexusBatch.npmIntegrity":
    "Les octets npm ne correspondent pas au shasum ou au SRI de la source. Ce paquet n’a pas été publié. Inspectez la source et réanalysez.",
  "nexusBatch.npmIdentity":
    "Les identités du composant, du packument et de l’archive npm diffèrent. Ce paquet n’a pas été publié. Vérifiez le nom et la version du paquet à la source.",
  "nexusBatch.scanGraph": "Validation du graphe complet des dépendances OCI",
  "nexusBatch.scanInterrupted":
    "L’analyse a été interrompue et n’a jamais été scellée. De nouvelles preuves de capacité permettent une réanalyse complète du même lot.",
  "nexusBatch.pageFailure":
    "La pagination de l’inventaire est incomplète ou répétée. Actualisez et relisez; il ne sera pas exporté comme inventaire complet.",
  "nexusBatch.targetConflict":
    "La cible a une identité ou des octets différents. Elle a été conservée et signalée comme un conflit.",
  "nexusBatch.unsupportedReason":
    "L’entrée ne satisfait pas aux exigences de migration complète de son format natif. Elle reste non prise en charge et exige un traitement distinct.",
};
