"use strict";

function clearList(list) {
	while (list.lastChild) {
		list.removeChild(list.lastChild);
	};
};

function sortByTree(list, classes, parents) {
	clearList(list);
	for (let item of parents) {
		item[1].appendChild(item[0]);
	};
};

function sortByName(list, classes, parents) {
	clearList(list);
	for (let item of classes) {
		list.appendChild(item[0]);
	};
};

function initSortClasses() {
	let list = document.getElementById("class-list");
	if (list === null) {
		return;
	};
	let classes = [];
	let parents = [];
	for (let li of list.querySelectorAll("li")) {
		classes.push([li, li.querySelector(".element-link").text]);
		parents.push([li, li.parentNode]);
	};
	classes.sort(function(a, b) {
		return a[1].localeCompare(b[1]);
	});

	let methods = [
		[sortByTree, "Tree", true],
		[sortByName, "Name"]
	];

	let controls = document.createElement("div");
	controls.className = "class-list-controls";
	list.insertAdjacentElement("beforebegin", controls);
	for (let method of methods) {
		let input = document.createElement("input");
		input.type = "radio";
		input.id = "class-sort-" + method[1];
		input.name = "sort";
		input.value = method[1];
		input.checked = method[2];
		controls.appendChild(input);
		let label = document.createElement("label");
		label.htmlFor = input.id;
		label.appendChild(document.createTextNode(method[1]));
		controls.appendChild(label);
		input.addEventListener("click", function(event) {
			method[0](list, classes, parents);
		});
	};
};

document.addEventListener("DOMContentLoaded", initSortClasses);
