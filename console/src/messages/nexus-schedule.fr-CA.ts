import type { nexusScheduleEnCA } from "./nexus-schedule.en-CA";

export const nexusScheduleFrCA: Record<keyof typeof nexusScheduleEnCA, string> =
  {
    "nexusSchedule.title": "Plans de migration récurrents",
    "nexusSchedule.new": "Créer un plan récurrent",
    "nexusSchedule.refresh": "Actualiser les plans",
    "nexusSchedule.background": "Les plans s’exécutent sur le serveur",
    "nexusSchedule.backgroundHint":
      "Chaque occurrence énumère entièrement la source et crée un lot incrémental durable. Fermer le navigateur n’a aucun effet. Les occurrences manquées sont fusionnées en une seule, et les lots ne se chevauchent jamais.",
    "nexusSchedule.capacityBoundary":
      "Chaque occurrence exige une nouvelle observation de capacité",
    "nexusSchedule.capacityBoundaryHint":
      "Aucun observateur fiable du pool d’objets distant n’est encore disponible. Chaque observation signalée par un administrateur autorise une occurrence. Les observations manquantes ou expirées suspendent la répartition jusqu’à leur actualisation; la migration n’est pas entièrement automatisée.",
    "nexusSchedule.empty": "Aucun plan récurrent",
    "nexusSchedule.emptyHint":
      "Terminez un lot de migration avec succès, puis utilisez sa version source et ses correspondances de dépôts pour créer un plan.",
    "nexusSchedule.template": "Lot réussi",
    "nexusSchedule.templateHint":
      "Le plan fige la version source, les correspondances et la politique du lot. Des liaisons modifiées exigent un nouveau lot réussi et un nouveau plan.",
    "nexusSchedule.interval": "Intervalle (minutes)",
    "nexusSchedule.createHint":
      "Les nouveaux plans attendent une nouvelle observation de capacité. Une fois celle-ci fournie, un plan arrivé à échéance répartit automatiquement une occurrence.",
    "nexusSchedule.active": "Activé",
    "nexusSchedule.waiting_capacity": "En attente d’une nouvelle capacité",
    "nexusSchedule.paused": "Suspendu",
    "nexusSchedule.blocked": "Demande attention",
    "nexusSchedule.stopped": "Arrêté",
    "nexusSchedule.nextRun": "Prochaine occurrence admissible",
    "nexusSchedule.lastSuccessful": "Dernière synchronisation réussie",
    "nexusSchedule.successScope":
      "Le succès couvre le contenu natif vérifié dans ce plan. Les métadonnées Maven régénérées restent dans le rapport du lot d’origine; cela ne signifie pas que toute la source est validée ni que la bascule des clients est vérifiée.",
    "nexusSchedule.never": "Aucune synchronisation réussie",
    "nexusSchedule.noNext": "Arrêté; aucune autre occurrence",
    "nexusSchedule.intervalValue": "Toutes les {minutes} minutes",
    "nexusSchedule.currentBatch": "Afficher le lot actif",
    "nexusSchedule.lastBatch": "Afficher le dernier lot",
    "nexusSchedule.pause": "Suspendre le plan",
    "nexusSchedule.resume": "Actualiser la capacité et reprendre",
    "nexusSchedule.capacity": "Fournir une observation de capacité",
    "nexusSchedule.stop": "Arrêter le plan",
    "nexusSchedule.stopConfirm":
      "L’arrêt empêche définitivement la création de nouveaux lots. Le lot actif et les artefacts publiés sont conservés.",
    "nexusSchedule.stopHint":
      "La suspension et l’arrêt touchent les lots futurs. Gérez le lot actif séparément sur sa page de lot.",
    "nexusSchedule.saveCapacity": "Soumettre la nouvelle observation",
    "nexusSchedule.blockedHint":
      "Vérifiez le dernier lot. Un changement de source ou de correspondances exige un nouveau plan fondé sur un nouveau lot réussi.",
    "nexusSchedule.mappingBusy":
      "Une cible a déjà un lot actif. Réglez-le avant de reprendre explicitement ce plan.",
    "nexusSchedule.mappingExists":
      "Ces cibles ont déjà un plan récurrent non arrêté. Gérez plutôt ce plan.",
    "nexusSchedule.templateRequired":
      "Sélectionnez un lot terminé et entièrement réussi, sans exception bloquante.",
    "nexusSchedule.dispatchFailed":
      "Échec de la répartition. Les nouvelles tentatives automatiques sont limitées à trois; une intervention est ensuite nécessaire.",
    "nexusSchedule.policyChanged":
      "La politique a changé. Créez un nouveau plan fondé sur un nouveau lot réussi.",
  };
