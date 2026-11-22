"""NIR local SGD with an immutable FedProx reference for the whole task."""
import math

import torch
from torch.nn import functional as F


def proximal_penalty(parameters, reference, mu):
    return 0.5 * mu * sum((parameter - anchor).square().sum()
                         for parameter, anchor in zip(parameters, reference))


def train(model, features, labels, *, algorithm, mu, epochs, batch_size,
          learning_rate, seed, round_no, client_index):
    if algorithm not in {"fedavg", "fedprox"}:
        raise ValueError("unsupported algorithm")
    if not math.isfinite(mu) or mu < 0 or (algorithm == "fedavg" and mu != 0):
        raise ValueError("invalid proximal coefficient")
    if epochs < 1 or batch_size < 1 or not 0 < learning_rate <= 1000:
        raise ValueError("invalid training parameters")
    if len(labels) == 0 or len(features) != len(labels):
        raise ValueError("invalid local dataset")
    reference = [parameter.detach().clone() for parameter in model.parameters()]
    generator = torch.Generator().manual_seed(
        seed * 100000 + (round_no - 1) * 100 + client_index)
    model.train()
    for _ in range(epochs):
        for indices in torch.randperm(len(labels), generator=generator).split(batch_size):
            model.zero_grad(set_to_none=True)
            loss = F.cross_entropy(model(features[indices]), labels[indices])
            if mu:
                loss = loss + proximal_penalty(model.parameters(), reference, mu)
            loss.backward()
            with torch.no_grad():
                for parameter in model.parameters():
                    parameter.add_(parameter.grad, alpha=-learning_rate)
    return len(labels)
