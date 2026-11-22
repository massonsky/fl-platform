"""Global metrics on a designated evaluation worker, never averaged F1."""
import numpy as np
import torch
from sklearn.metrics import confusion_matrix, f1_score
from torch.nn import functional as F


def evaluate(model, features, labels):
    model.eval()
    with torch.no_grad():
        logits = model(features)
        predictions = logits.argmax(1).cpu().numpy()
        loss = float(F.cross_entropy(logits, torch.as_tensor(labels)))
    return {"accuracy": float(np.mean(predictions == labels)),
            "macro_f1": float(f1_score(labels, predictions, labels=list(range(10)),
                                      average="macro", zero_division=0)),
            "cross_entropy": loss,
            "confusion_matrix": confusion_matrix(labels, predictions,
                                                  labels=list(range(10))).tolist()}
